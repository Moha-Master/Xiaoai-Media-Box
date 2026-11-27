"""歌词实体：state 就是“当前正在唱的那一行”。

时间轴完全由 xiaoai-mb 算（它本来就有播放进度，探针每 status_poll 秒纠偏一次），
HA 这边不做任何时间推算、也不自己轮询——直接消费 coordinator 里的投影，
更新由 SSE 的 lyric 帧驱动（流断了才退回兜底轮询）。

用 sensor 而不是 text：text 是“可写输入框”语义，这里只读。
"""

from __future__ import annotations

import logging
from typing import Any

from homeassistant.components.sensor import SensorEntity
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.helpers.entity_platform import AddEntitiesCallback
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from .const import DOMAIN
from .coordinator import XiaoAiCoordinator

_LOGGER = logging.getLogger(__name__)


async def async_setup_entry(
    hass: HomeAssistant,
    entry: ConfigEntry,
    async_add_entities: AddEntitiesCallback,
) -> None:
    coordinator: XiaoAiCoordinator = hass.data[DOMAIN][entry.entry_id]
    async_add_entities(
        [XiaoAiLyricSensor(coordinator, entry, sid) for sid in coordinator.speakers]
    )


class XiaoAiLyricSensor(CoordinatorEntity[XiaoAiCoordinator], SensorEntity):
    """一台音箱一个歌词实体，state = 正在唱的那一行。"""

    _attr_has_entity_name = True
    _attr_name = "歌词"
    _attr_icon = "mdi:music-note-text"

    def __init__(
        self, coordinator: XiaoAiCoordinator, entry: ConfigEntry, speaker_id: str
    ) -> None:
        super().__init__(coordinator)
        self._entry = entry
        self._speaker_id = speaker_id
        self._attr_unique_id = f"{entry.entry_id}_{speaker_id}_lyric"

    @property
    def device_info(self) -> DeviceInfo:
        return DeviceInfo(
            identifiers={(DOMAIN, f"{self._entry.entry_id}_{self._speaker_id}")}
        )

    @property
    def _data(self) -> dict[str, Any]:
        return (self.coordinator.data or {}).get(self._speaker_id) or {}

    @property
    def _lyric(self) -> dict[str, Any]:
        return self._data.get("lyric") or {}

    @property
    def available(self) -> bool:
        # 统一走 coordinator.speaker_available（含 unreachable 判定）
        return self.coordinator.speaker_available(self._speaker_id)

    @property
    def native_value(self) -> str | None:
        return self._lyric.get("line") or None

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        snap = self._lyric
        total = snap.get("total") or 0
        # has_lyric 三态：
        #   True / False：后端已给过这首歌的歌词快照，total 判断有无歌词；
        #   None：还没拿到快照（没在播 / 轮询拉取失败 / SSE 未推过）——
        #         「暂时不知道」而不是「这首歌没歌词」。
        # 注意后端在「拉取失败」和「这首歌真没歌词」时返回同形（song_id 有值 + total=0），
        # HTTP 层的失败已在 coordinator 里区分，这两者无法进一步区分。
        if snap.get("song_id"):
            has_lyric: bool | None = total > 0
        else:
            has_lyric = None
        return {
            "speaker_id": self._speaker_id,
            "song_id": snap.get("song_id") or None,
            "has_lyric": has_lyric,
            "line_index": snap.get("index"),
            "line_count": total,
            "next_line": snap.get("next_line") or None,
            "position_ms": snap.get("position_ms"),
        }
