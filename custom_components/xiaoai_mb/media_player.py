from __future__ import annotations

import logging
from typing import Any
from urllib.parse import quote

import aiohttp
from homeassistant.components.media_player import (
    BrowseMedia,
    MediaClass,
    MediaPlayerEnqueue,
    MediaPlayerEntity,
    MediaPlayerEntityFeature,
    MediaPlayerState,
    MediaType,
    RepeatMode,
    SearchMedia,
    SearchMediaQuery,
)
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant, callback
from homeassistant.exceptions import HomeAssistantError
from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.helpers.entity_platform import AddEntitiesCallback
from homeassistant.helpers.update_coordinator import CoordinatorEntity
from homeassistant.util import dt as dt_util

from .const import DOMAIN
from .coordinator import XiaoAiCoordinator

_LOGGER = logging.getLogger(__name__)

FEATURES = (
    MediaPlayerEntityFeature.PLAY
    | MediaPlayerEntityFeature.PAUSE
    | MediaPlayerEntityFeature.STOP
    | MediaPlayerEntityFeature.NEXT_TRACK
    | MediaPlayerEntityFeature.PREVIOUS_TRACK
    | MediaPlayerEntityFeature.BROWSE_MEDIA
    | MediaPlayerEntityFeature.SEARCH_MEDIA
    | MediaPlayerEntityFeature.PLAY_MEDIA
    | MediaPlayerEntityFeature.VOLUME_SET
    | MediaPlayerEntityFeature.VOLUME_MUTE
    | MediaPlayerEntityFeature.SHUFFLE_SET
    | MediaPlayerEntityFeature.REPEAT_SET
    | MediaPlayerEntityFeature.SEEK
    | MediaPlayerEntityFeature.MEDIA_ENQUEUE
)

LOOP_TO_REPEAT = {
    "none": RepeatMode.OFF,
    "list": RepeatMode.ALL,
    "single": RepeatMode.ONE,
}

REPEAT_ARG = {
    RepeatMode.OFF: "off",
    RepeatMode.ALL: "all",
    RepeatMode.ONE: "one",
}


def _parse_media_id(media_id: str) -> dict[str, Any]:
    """Map a BrowseMedia / Assist media id to xiaoai-mb's POST /api/play body."""
    s = (media_id or "").strip()
    if s == "daily":
        return {"daily": True}
    if ":" in s:
        prefix, rest = s.split(":", 1)
        prefix = prefix.lower()
        if prefix in ("song", "track"):
            try:
                return {"song_ids": [int(rest)]}
            except ValueError:
                pass
        elif prefix == "playlist":
            return {"playlist_id": rest}
        elif prefix == "album":
            return {"album_id": rest}
        elif prefix == "search":
            return {"query": rest}
    # 纯文本：Assist / 手动调用都当搜索
    return {"query": s}


def _track_title(t: dict) -> str:
    title = t.get("name") or "未知曲目"
    if t.get("artists"):
        title = f"{title} - {t['artists']}"
    return title


def _track_browse(t: dict) -> BrowseMedia:
    return BrowseMedia(
        media_class=MediaClass.TRACK,
        media_content_id=f"song:{t.get('id')}",
        media_content_type=MediaType.TRACK,
        title=_track_title(t),
        can_play=True,
        can_expand=False,
        thumbnail=t.get("cover"),
    )


async def async_setup_entry(
    hass: HomeAssistant,
    entry: ConfigEntry,
    async_add_entities: AddEntitiesCallback,
) -> None:
    coordinator: XiaoAiCoordinator = hass.data[DOMAIN][entry.entry_id]
    async_add_entities(
        [XiaoAiMusicPlayer(coordinator, entry, sid) for sid in coordinator.speakers]
    )


class XiaoAiMusicPlayer(CoordinatorEntity[XiaoAiCoordinator], MediaPlayerEntity):
    """One speaker driven by one xiaoai-mb session."""

    _attr_has_entity_name = True
    _attr_name = None
    _attr_supported_features = FEATURES

    def __init__(
        self, coordinator: XiaoAiCoordinator, entry: ConfigEntry, speaker_id: str
    ) -> None:
        super().__init__(coordinator)
        self._entry = entry
        self._speaker_id = speaker_id
        self._speaker_name = coordinator.speaker_names.get(speaker_id, speaker_id)
        self._position_updated_at = None
        self._attr_unique_id = f"{entry.entry_id}_{speaker_id}"

    # ---------- 基础状态 ----------

    @property
    def available(self) -> bool:
        # 可用性判断统一收在 coordinator.speaker_available，
        # 四个实体类型共用一条，避免出现"播放器不可用但歌词还在跳"
        return self.coordinator.speaker_available(self._speaker_id)

    @property
    def device_info(self) -> DeviceInfo:
        return DeviceInfo(
            identifiers={(DOMAIN, f"{self._entry.entry_id}_{self._speaker_id}")},
            name=self._speaker_name,
            manufacturer="Xiaomi",
            model="Xiaoai Media Box",
            configuration_url=self.coordinator.base_url,
        )

    @property
    def _data(self) -> dict:
        return (self.coordinator.data or {}).get(self._speaker_id) or {}

    @property
    def _queue(self) -> dict:
        return self._data.get("queue") or {}

    @property
    def _device(self) -> dict:
        return self._data.get("device") or {}

    @property
    def _current(self) -> dict | None:
        return self._queue.get("current")

    @property
    def state(self) -> MediaPlayerState | None:
        # 设备不可达 → 未知。绝不能回落到「暂停」或「空闲」：那会把
        # 「问不到」谎报成「知道它没在放」，自动化会据此做错决定。
        if self._data.get("unreachable"):
            return None
        device = self._device
        queue = self._queue
        if device.get("playing") or queue.get("playing"):
            return MediaPlayerState.PLAYING
        if device.get("status") == 2:
            return MediaPlayerState.PAUSED
        if queue.get("current"):
            return MediaPlayerState.PAUSED
        return MediaPlayerState.IDLE

    @property
    def media_title(self) -> str | None:
        cur = self._current
        return cur.get("name") if cur else None

    @property
    def media_artist(self) -> str | None:
        cur = self._current
        return cur.get("artists") if cur else None

    @property
    def media_album_name(self) -> str | None:
        cur = self._current
        return cur.get("album") if cur else None

    @property
    def media_playlist(self) -> str | None:
        return self._queue.get("source") or None

    @property
    def entity_picture(self) -> str | None:
        cur = self._current
        return cur.get("cover") if cur else None

    @property
    def media_duration(self) -> float | None:
        cur = self._current
        if not cur:
            return None
        ms = cur.get("duration_ms") or self._queue.get("duration_ms") or 0
        return ms / 1000 if ms else None

    @property
    def media_position(self) -> float | None:
        if not self._current:
            return None
        return (self._queue.get("position_ms") or 0) / 1000

    @property
    def media_position_updated_at(self):
        return self._position_updated_at

    @property
    def volume_level(self) -> float | None:
        volume = self._data.get("volume")
        if not isinstance(volume, (int, float)):
            volume = self._device.get("volume")
        return volume / 100 if isinstance(volume, (int, float)) else None

    @property
    def is_volume_muted(self) -> bool:
        return bool(self._data.get("muted"))

    @property
    def media_content_id(self) -> str | None:
        cur = self._current
        return f"song:{cur.get('id')}" if cur else None

    @property
    def media_content_type(self) -> MediaType | None:
        return MediaType.MUSIC if self._current else None

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        cur = self._current or {}
        hot = self._data.get("hotmode") or {}
        attrs: dict[str, Any] = {
            "speaker_id": self._speaker_id,
            "speaker_name": self._speaker_name,
            "daemon_url": self.coordinator.base_url,
            "queue_source": self._queue.get("source"),
            "device_status": self._device.get("status"),
            "device_poll_sec": self._device.get("poll_sec"),
            "volume": self._data.get("volume"),
            "queue_index": self._queue.get("index"),
            "queue_total": self._queue.get("total"),
            "repeat_mode": self._queue.get("repeat") or self._queue.get("loop"),
            "shuffle": self.shuffle,
            "liked": bool(self._queue.get("liked")),
            # 心动续杯：on / 累计续杯次数 / 停止原因（give-up 后 UI 能说清“为什么没了”）
            "hotmode_on": bool(hot.get("on")),
            "hotmode_extended": hot.get("extended") or 0,
            "hotmode_stop_reason": hot.get("stop_reason") or None,
            # 当前曲试听区间（原曲时间轴毫秒；null = 全曲可播）。
            # media_position / media_duration 同样用原曲轴，与服务端一致。
            "trial_start_ms": cur.get("trial_start_ms") or None,
            "trial_end_ms": cur.get("trial_end_ms") or None,
            # poll（回读节奏档位）只在 daemon 的 /api/state 与 WebUI 看，
            # 放进实体属性会带来无意义的 state_changed 写入
            "event_stream": "connected" if self.coordinator.stream_ok else "polling",
        }
        return attrs

    @property
    def shuffle(self) -> bool | None:
        queue = self._queue
        if "shuffle" in queue:
            return bool(queue.get("shuffle"))
        # 兼容老版本 daemon：只有 loop=shuffle 这一个容器
        return queue.get("loop") == "shuffle"

    @property
    def repeat(self) -> RepeatMode:
        queue = self._queue
        # 新 daemon 的 "repeat" 与随机正交；老版本只有 "loop"（shuffle 会盖住循环语义）
        mode = (queue.get("repeat") or queue.get("loop") or "list").lower()
        return LOOP_TO_REPEAT.get(mode, RepeatMode.ALL)

    @callback
    def _handle_coordinator_update(self) -> None:
        self._position_updated_at = dt_util.utcnow()
        super()._handle_coordinator_update()

    async def async_added_to_hass(self) -> None:
        await super().async_added_to_hass()
        self._position_updated_at = dt_util.utcnow()

    # ---------- HTTP helpers ----------

    async def _get(self, path: str, params: dict | None = None) -> dict:
        timeout = aiohttp.ClientTimeout(total=20)
        async with self.coordinator.session.get(
            f"{self.coordinator.base_url}{path}", params=params, timeout=timeout
        ) as resp:
            if resp.status != 200:
                text = await resp.text()
                raise HomeAssistantError(f"xiaoai-mb {path} HTTP {resp.status}: {text[:200]}")
            return await resp.json()

    async def _post(self, path: str, body: dict | None = None) -> dict:
        sep = "&" if "?" in path else "?"
        url = (
            f"{self.coordinator.base_url}{path}{sep}"
            f"speaker={quote(self._speaker_id, safe='')}"
        )
        timeout = aiohttp.ClientTimeout(total=25)
        async with self.coordinator.session.post(
            url, json=body or {}, timeout=timeout
        ) as resp:
            if resp.status >= 400:
                text = await resp.text()
                raise HomeAssistantError(f"xiaoai-mb {path} HTTP {resp.status}: {text[:200]}")
            return await resp.json()

    # ---------- 传输控制 ----------

    async def async_media_play(self) -> None:
        await self._post("/api/resume")
        await self.coordinator.async_refresh_fresh(self._speaker_id)

    async def async_media_pause(self) -> None:
        await self._post("/api/pause")
        await self.coordinator.async_refresh_fresh(self._speaker_id)

    async def async_media_stop(self) -> None:
        await self._post("/api/stop")
        await self.coordinator.async_refresh_fresh(self._speaker_id)

    async def async_media_next_track(self) -> None:
        await self._post("/api/next")
        await self.coordinator.async_refresh_fresh(self._speaker_id)

    async def async_media_previous_track(self) -> None:
        await self._post("/api/prev")
        await self.coordinator.async_refresh_fresh(self._speaker_id)

    async def async_set_volume_level(self, volume: float) -> None:
        value = max(0, min(100, round(volume * 100)))
        await self._post("/api/volume", {"value": value})
        await self.coordinator.async_refresh_fresh(self._speaker_id)

    async def async_mute_volume(self, mute: bool) -> None:
        await self._post("/api/mute", {"value": mute})
        await self.coordinator.async_refresh_fresh(self._speaker_id)

    async def async_media_seek(self, position: float) -> None:
        await self._post("/api/seek", {"position_ms": max(0, round(position * 1000))})
        await self.coordinator.async_refresh_fresh(self._speaker_id)

    async def async_set_shuffle(self, shuffle: bool) -> None:
        await self._post("/api/mode", {"shuffle": shuffle})
        await self.coordinator.async_refresh_fresh(self._speaker_id)

    async def async_set_repeat(self, repeat: RepeatMode) -> None:
        mode = REPEAT_ARG.get(repeat)
        if mode is None:
            raise HomeAssistantError(f"不支持的播放模式: {repeat}")
        await self._post("/api/mode", {"repeat": mode})
        await self.coordinator.async_refresh_fresh(self._speaker_id)

    async def async_play_media(
        self, media_type: MediaType | str, media_id: str, **kwargs: Any
    ) -> None:
        if media_id.startswith("hotmode:"):
            seed = media_id.split(":", 1)[1].strip() or "current"
            if seed not in ("current", "liked"):
                raise HomeAssistantError(f"无效的心动种子: {seed}")
            await self._post("/api/hotmode", {"seed": seed})
        elif media_id.startswith("jump:"):
            try:
                index = int(media_id.split(":", 1)[1])
            except ValueError as err:
                raise HomeAssistantError(f"无效的队列下标: {media_id}") from err
            await self._post("/api/jump", {"index": index})
        else:
            body = _parse_media_id(media_id)
            enqueue = kwargs.get("enqueue")
            if enqueue in (MediaPlayerEnqueue.NEXT, MediaPlayerEnqueue.ADD):
                body["next"] = enqueue == MediaPlayerEnqueue.NEXT
                await self._post("/api/enqueue", body)
            else:
                await self._post("/api/play", body)
        await self.coordinator.async_refresh_fresh(self._speaker_id)

    # ---------- 浏览 / 搜索 ----------

    async def async_browse_media(
        self,
        media_content_type: MediaType | str | None = None,
        media_content_id: str | None = None,
    ) -> BrowseMedia:
        if not media_content_id or media_content_id == "root":
            return BrowseMedia(
                media_class=MediaClass.DIRECTORY,
                media_content_id="root",
                media_content_type="directory",
                title=self._speaker_name,
                can_play=False,
                can_expand=True,
                can_search=True,
                search_media_classes=[MediaClass.TRACK, MediaClass.PLAYLIST],
                children=[
                    BrowseMedia(
                        media_class=MediaClass.DIRECTORY,
                        media_content_id="search",
                        media_content_type="directory",
                        title="全局搜索",
                        can_play=False,
                        can_expand=True,
                        can_search=True,
                        search_media_classes=[
                            MediaClass.TRACK,
                            MediaClass.PLAYLIST,
                        ],
                    ),
                    BrowseMedia(
                        media_class=MediaClass.PLAYLIST,
                        media_content_id="queue",
                        media_content_type=MediaType.PLAYLIST,
                        title="当前队列",
                        can_play=False,
                        can_expand=True,
                    ),
                    BrowseMedia(
                        media_class=MediaClass.PLAYLIST,
                        media_content_id="daily",
                        media_content_type=MediaType.PLAYLIST,
                        title="每日推荐",
                        can_play=True,
                        can_expand=True,
                    ),
                    BrowseMedia(
                        media_class=MediaClass.PLAYLIST,
                        media_content_id="hotmode:current",
                        media_content_type=MediaType.PLAYLIST,
                        title="心动·当前",
                        can_play=True,
                        can_expand=False,
                    ),
                    BrowseMedia(
                        media_class=MediaClass.PLAYLIST,
                        media_content_id="hotmode:liked",
                        media_content_type=MediaType.PLAYLIST,
                        title="心动·红心",
                        can_play=True,
                        can_expand=False,
                    ),
                    BrowseMedia(
                        media_class=MediaClass.DIRECTORY,
                        media_content_id="playlists",
                        media_content_type="directory",
                        title="我的歌单",
                        can_play=False,
                        can_expand=True,
                    ),
                ],
            )

        if media_content_id == "search":
            # 搜索框由前端在“进入这个节点后”渲染；结果由 async_search_media 给，
            # 后端永远是全网搜索，不做节点内过滤。
            return BrowseMedia(
                media_class=MediaClass.DIRECTORY,
                media_content_id="search",
                media_content_type="directory",
                title="全局搜索",
                can_play=False,
                can_expand=True,
                can_search=True,
                search_media_classes=[MediaClass.TRACK, MediaClass.PLAYLIST],
                children=[],
            )

        if media_content_id == "queue":
            data = await self._get(
                "/api/queue", params={"speaker": self._speaker_id}
            )
            q = data.get("queue") or {}
            tracks = q.get("tracks") or []
            index = q.get("index", -1)
            children = []
            for i, t in enumerate(tracks):
                title = _track_title(t)
                if i == index:
                    title = "▶ " + title
                children.append(
                    BrowseMedia(
                        media_class=MediaClass.TRACK,
                        media_content_id=f"jump:{i}",
                        media_content_type=MediaType.TRACK,
                        title=title,
                        can_play=True,
                        can_expand=False,
                        thumbnail=t.get("cover"),
                    )
                )
            return BrowseMedia(
                media_class=MediaClass.PLAYLIST,
                media_content_id="queue",
                media_content_type=MediaType.PLAYLIST,
                title="当前队列",
                can_play=False,
                can_expand=True,
                children=children,
            )

        if media_content_id == "playlists":
            data = await self._get("/api/user/playlists")
            children = [
                BrowseMedia(
                    media_class=MediaClass.PLAYLIST,
                    media_content_id=f"playlist:{p.get('id')}",
                    media_content_type=MediaType.PLAYLIST,
                    title=p.get("name") or str(p.get("id")),
                    can_play=True,
                    can_expand=True,
                    thumbnail=p.get("cover"),
                )
                for p in data.get("playlists", [])
            ]
            return BrowseMedia(
                media_class=MediaClass.DIRECTORY,
                media_content_id="playlists",
                media_content_type="directory",
                title="我的歌单",
                can_play=False,
                can_expand=True,
                children=children,
            )

        if media_content_id == "daily":
            data = await self._get("/api/daily")
            tracks = data.get("tracks", [])
            return BrowseMedia(
                media_class=MediaClass.PLAYLIST,
                media_content_id="daily",
                media_content_type=MediaType.PLAYLIST,
                title="每日推荐",
                can_play=True,
                can_expand=True,
                children=[_track_browse(t) for t in tracks],
            )

        if media_content_id.startswith("playlist:"):
            playlist_id = media_content_id.split(":", 1)[1]
            data = await self._get(f"/api/playlist/{playlist_id}")
            tracks = data.get("tracks", [])
            return BrowseMedia(
                media_class=MediaClass.PLAYLIST,
                media_content_id=media_content_id,
                media_content_type=MediaType.PLAYLIST,
                title=f"歌单 {playlist_id}",
                can_play=True,
                can_expand=True,
                children=[_track_browse(t) for t in tracks],
            )

        return BrowseMedia(
            media_class=MediaClass.DIRECTORY,
            media_content_id=media_content_id,
            media_content_type="directory",
            title="未知项",
            can_play=False,
            can_expand=False,
        )

    async def async_search_media(self, query: SearchMediaQuery) -> SearchMedia:
        data = await self._get(
            "/api/search", params={"q": query.search_query, "limit": 30}
        )
        return SearchMedia(result=[_track_browse(t) for t in data.get("tracks", [])])
