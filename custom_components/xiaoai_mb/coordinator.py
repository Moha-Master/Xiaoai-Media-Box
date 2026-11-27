"""Coordinator：消费 xiaoai-mb 的 SSE 事件流，退化为兜底轮询。

设计要点：**xiaoai-mb 是唯一权威状态源**。

- 设备端不会主动上报（实测见 `docs/MEDIA-ARCH.md` §8.4.3），但 xiaoai-mb 手里有真值，
  而且它的调度循环每 500ms 就能发现变化 → 由它通过 SSE 推给 HA；
- 流活着的时候 `_async_update_data` **不发任何 HTTP**，直接返回缓存；
- 流断了（重启 daemon、网络抖动）自动指数退避重连，期间退回 `DEFAULT_SCAN_INTERVAL`
  轮询，并把歌词也一起取回来，功能不会瘸。
"""

from __future__ import annotations

import asyncio
import json
import logging
from datetime import timedelta
from typing import Any
from urllib.parse import quote

import aiohttp
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.helpers.update_coordinator import (
    DataUpdateCoordinator,
    UpdateFailed,
)

from .const import DEFAULT_SCAN_INTERVAL, DOMAIN

_LOGGER = logging.getLogger(__name__)

_RECONNECT_MIN = 2
_RECONNECT_MAX = 30


class XiaoAiCoordinator(DataUpdateCoordinator[dict]):
    """Hold the per-speaker snapshot that xiaoai-mb pushes (or we poll)."""

    def __init__(
        self, hass: HomeAssistant, entry: ConfigEntry, session: aiohttp.ClientSession
    ) -> None:
        self.base_url: str = entry.data["url"].rstrip("/")
        # 兼容旧版单音箱配置条目
        self.speakers: list[str] = list(
            entry.data.get("speakers")
            or [entry.data.get("speaker_id", "default")]
        )
        self.speaker_names: dict[str, str] = dict(
            entry.data.get("speaker_names")
            or {
                entry.data.get("speaker_id", "default"): entry.data.get(
                    "speaker_name", "小爱音箱"
                )
            }
        )
        self.session = session
        self._stream_task: asyncio.Task | None = None
        self._stream_ok = False
        super().__init__(
            hass,
            _LOGGER,
            name=f"{DOMAIN}_{len(self.speakers)}spk",
            update_interval=timedelta(seconds=DEFAULT_SCAN_INTERVAL),
        )

    def speaker_available(self, speaker_id: str) -> bool:
        """这台音箱的实体是否可用。

        四个实体类型（media_player / switch / sensor / number）共用这一条判断，
        免得各写各的、哪天漏一处就出现「播放器不可用但歌词传感器还在跳」。

        服务端可达 ≠ 音箱可达：`unreachable` 是设备主动上报静默超时，
        此时所有读数都是冻结的旧值，必须整体不可用，而不是继续显示
        「正在播放 + 某个进度」——那会把「问不到」谎报成「知道它在放什么」。
        """
        if not self.last_update_success:
            return False
        data = self.data or {}
        if speaker_id not in data:
            return False
        return not (data.get(speaker_id) or {}).get("unreachable")

    @property
    def stream_ok(self) -> bool:
        """事件流是否在线（在线就不用轮询）。"""
        return self._stream_ok

    # ---------- 事件流 ----------

    def async_start_stream(self) -> None:
        if self._stream_task is not None and not self._stream_task.done():
            return
        self._stream_task = self.hass.async_create_background_task(
            self._stream_loop(), f"{DOMAIN}_stream"
        )

    async def async_stop_stream(self) -> None:
        task, self._stream_task = self._stream_task, None
        self._stream_ok = False
        if task is not None:
            task.cancel()
            try:
                await task
            except asyncio.CancelledError:
                pass

    async def _stream_loop(self) -> None:
        """读 xiaoai-mb 的 /api/events（SSE）。断开就退避重连。"""
        backoff = _RECONNECT_MIN
        while True:
            try:
                await self._read_stream()
            except asyncio.CancelledError:
                self._stream_ok = False
                raise
            except Exception as err:  # noqa: BLE001 - 任何异常都只影响新鲜度
                self._stream_ok = False
                _LOGGER.debug("xiaoai-mb 事件流断开，%ss 后重连：%s", backoff, err)
                await asyncio.sleep(backoff)
                backoff = min(backoff * 2, _RECONNECT_MAX)

    async def _read_stream(self) -> None:
        url = f"{self.base_url}/api/events"
        # 长连接：不设总超时，只给单次读留足心跳间隔（daemon 每 20s ping 一次）
        timeout = aiohttp.ClientTimeout(total=None, connect=15, sock_read=90)
        async with self.session.get(url, timeout=timeout) as resp:
            if resp.status != 200:
                text = await resp.text()
                raise UpdateFailed(f"xiaoai-mb /api/events HTTP {resp.status}: {text[:120]}")
            self._stream_ok = True
            _LOGGER.debug("xiaoai-mb 事件流已连接：%s", url)
            topic: str | None = None
            data_lines: list[str] = []
            async for raw in resp.content:
                line = raw.decode("utf-8", "replace").rstrip("\r\n")
                if not line:  # 空行 = 一帧结束
                    if data_lines:
                        self._apply_event(topic, "\n".join(data_lines))
                    topic, data_lines = None, []
                    continue
                if line.startswith(":"):  # 心跳注释
                    continue
                if line.startswith("event:"):
                    topic = line[6:].strip()
                elif line.startswith("data:"):
                    data_lines.append(line[5:].strip())

    def _apply_event(self, topic: str | None, raw_data: str) -> None:
        try:
            payload = json.loads(raw_data)
        except ValueError:
            return
        if not isinstance(payload, dict):
            return
        speaker = payload.get("speaker")
        if speaker not in self.speakers:
            return
        cur = dict((self.data or {}).get(speaker) or {})
        if topic == "state":
            cur.update(
                {
                    "queue": payload.get("queue") or {},
                    "device": payload.get("device"),
                    "volume": payload.get("volume"),
                    "muted": bool(payload.get("muted")),
                    "alarm_volume": payload.get("alarm_volume"),
                    "poll": payload.get("poll") or {},
                    # 心动续杯状态（on/extended/stop_reason），给实体属性与自动化用
                    "hotmode": payload.get("hotmode"),
                    # 设备不可达（推送静默超时）：available 与 state 都消费它
                    "unreachable": bool(payload.get("unreachable")),
                }
            )
        elif topic == "lyric":
            cur["lyric"] = payload.get("lyric") or {}
        else:
            return
        merged = dict(self.data or {})
        merged[speaker] = cur
        self.async_set_updated_data(merged)

    # ---------- 兜底轮询 ----------

    async def _async_update_data(self) -> dict:
        if self._stream_ok:
            # 事件流在线：xiaoai-mb 推的快照就是最新真相，一次 HTTP 都不用发
            return self.data or {}
        out: dict[str, dict] = {}
        for sid in self.speakers:
            out[sid] = await self._fetch_one(sid, fresh=False)
        return out

    async def _fetch(self, *, fresh: bool) -> dict:
        out: dict[str, dict] = {}
        for sid in self.speakers:
            out[sid] = await self._fetch_one(sid, fresh=fresh)
        return out

    async def _fetch_one(self, speaker_id: str, *, fresh: bool) -> dict:
        url = f"{self.base_url}/api/state?speaker={quote(speaker_id, safe='')}"
        if fresh:
            url += "&fresh=1"
        timeout = aiohttp.ClientTimeout(total=20)
        try:
            async with self.session.get(url, timeout=timeout) as resp:
                if resp.status != 200:
                    text = await resp.text()
                    raise UpdateFailed(
                        f"xiaoai-mb /api/state HTTP {resp.status}: {text[:200]}"
                    )
                state = await resp.json()
        except aiohttp.ClientError as err:
            raise UpdateFailed(f"无法连接 xiaoai-mb: {err}") from err

        if not state.get("ok"):
            raise UpdateFailed(f"xiaoai-mb 返回错误: {state.get('error')}")

        prev = dict((self.data or {}).get(speaker_id) or {})
        prev_lyric = prev.get("lyric") or {}
        cur = (state.get("queue") or {}).get("current") or {}
        fetched = await self._fetch_lyric(speaker_id)
        if fetched is None:
            # 拉取失败：只有歌词确实属于当前这首歌才沿用上一帧。
            # 复用必须按歌判断——否则换歌瞬间失败/一次间歇故障会把旧歌歌词永久粘住
            # （歌词事故的帮凶之一，2026-11 修复）。不同歌宁可置空也不粘旧数据。
            same_song = bool(prev_lyric) and str(prev_lyric.get("song_id") or "") == str(
                cur.get("id") or ""
            )
            lyric = prev_lyric if same_song else {}
        else:
            lyric = fetched
        packed = {
            "queue": state.get("queue") or {},
            "device": state.get("device"),
            "volume": state.get("volume"),
            "muted": bool(state.get("muted")),
            "alarm_volume": state.get("alarm_volume"),
            "poll": state.get("poll") or {},
            "hotmode": state.get("hotmode"),
            "unreachable": bool(state.get("unreachable")),
            # 轮询模式下顺手取歌词；事件流模式下 _async_update_data 不会走到这里，
            # 歌词由 SSE 的 lyric 帧覆盖
            "lyric": lyric,
        }
        return packed

    async def _fetch_lyric(self, speaker_id: str) -> dict | None:
        """拉当前曲歌词快照。HTTP 失败返回 None（与「真的没有歌词」区分开）。"""
        url = f"{self.base_url}/api/lyric?speaker={quote(speaker_id, safe='')}"
        timeout = aiohttp.ClientTimeout(total=15)
        try:
            async with self.session.get(url, timeout=timeout) as resp:
                if resp.status != 200:
                    return None
                data = await resp.json()
        except aiohttp.ClientError:
            return None
        return data.get("lyric") or {}

    # ---------- 供实体调用的工具 ----------

    async def async_refresh_fresh(self, speaker_id: str) -> None:
        """Refresh one speaker immediately, keeping the other speakers' data."""
        data = await self._fetch_one(speaker_id, fresh=True)
        merged = dict(self.data or {})
        merged[speaker_id] = data
        self.async_set_updated_data(merged)

    async def async_post(
        self, speaker_id: str, path: str, body: dict | None = None
    ) -> dict:
        sep = "&" if "?" in path else "?"
        url = f"{self.base_url}{path}{sep}speaker={quote(speaker_id, safe='')}"
        timeout = aiohttp.ClientTimeout(total=25)
        async with self.session.post(url, json=body or {}, timeout=timeout) as resp:
            if resp.status >= 400:
                text = await resp.text()
                raise UpdateFailed(f"xiaoai-mb {path} HTTP {resp.status}: {text[:200]}")
            return await resp.json()

    async def async_get(
        self, speaker_id: str, path: str, params: dict | None = None
    ) -> dict[Any, Any]:
        query = {"speaker": speaker_id}
        if params:
            query.update(params)
        timeout = aiohttp.ClientTimeout(total=20)
        async with self.session.get(
            f"{self.base_url}{path}", params=query, timeout=timeout
        ) as resp:
            if resp.status != 200:
                text = await resp.text()
                raise UpdateFailed(f"xiaoai-mb {path} HTTP {resp.status}: {text[:200]}")
            return await resp.json()
