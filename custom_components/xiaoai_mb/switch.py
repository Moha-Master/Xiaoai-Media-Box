"""红心开关：显示并切换“当前这首歌”是否已收藏。

HA 的 media_player 没有 LIKE 这个 feature，所以红心只能做成独立实体。
这里用 switch 而不是 button，是因为需要“有状态”（心是实心还是空心）；
状态只从 xiaoai-mb 的 /api/state 投影出来，本地不做乐观更新，不存在两处状态打架。
"""

from __future__ import annotations

import logging
from typing import Any

from homeassistant.components.switch import SwitchEntity
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
        [XiaoAiLikedSwitch(coordinator, entry, sid) for sid in coordinator.speakers]
    )


class XiaoAiLikedSwitch(CoordinatorEntity[XiaoAiCoordinator], SwitchEntity):
    """一台音箱一个红心开关（对应当前曲目）。"""

    _attr_has_entity_name = True
    _attr_name = "红心"

    def __init__(
        self, coordinator: XiaoAiCoordinator, entry: ConfigEntry, speaker_id: str
    ) -> None:
        super().__init__(coordinator)
        self._entry = entry
        self._speaker_id = speaker_id
        self._speaker_name = coordinator.speaker_names.get(speaker_id, speaker_id)
        self._attr_unique_id = f"{entry.entry_id}_{speaker_id}_liked"

    @property
    def device_info(self) -> DeviceInfo:
        return DeviceInfo(
            identifiers={(DOMAIN, f"{self._entry.entry_id}_{self._speaker_id}")}
        )

    @property
    def _data(self) -> dict[str, Any]:
        return (self.coordinator.data or {}).get(self._speaker_id) or {}

    @property
    def _queue(self) -> dict[str, Any]:
        return self._data.get("queue") or {}

    @property
    def available(self) -> bool:
        return self.coordinator.speaker_available(self._speaker_id) and (
            self._queue.get("current") is not None
        )

    @property
    def is_on(self) -> bool:
        return bool(self._queue.get("liked"))

    @property
    def icon(self) -> str:
        return "mdi:heart" if self.is_on else "mdi:heart-outline"

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        cur = self._queue.get("current") or {}
        return {
            "speaker_id": self._speaker_id,
            "song_id": cur.get("id"),
            "song": cur.get("name"),
        }

    async def async_turn_on(self, **kwargs: Any) -> None:
        await self.coordinator.async_post(self._speaker_id, "/api/like", {"like": True})
        await self.coordinator.async_refresh_fresh(self._speaker_id)

    async def async_turn_off(self, **kwargs: Any) -> None:
        await self.coordinator.async_post(
            self._speaker_id, "/api/like", {"like": False}
        )
        await self.coordinator.async_refresh_fresh(self._speaker_id)
