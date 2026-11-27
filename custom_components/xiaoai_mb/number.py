"""闹钟音量滑块。

闹钟音量是设备上**独立于播放音量**的一套状态（alarmd 维护，存在
`/data/player/alarmvolume.cfg`），所以不能挂在 media_player 的 volume_level 上，
必须单独一个 number 实体。

MIoT 规范里没有这个能力（实测型号的 spec 只有 speaker.volume 和 clock.stop-alarm），
手机 App 是走云端 ubus 代理改的；后端 xiaoai-mb 用同一通道（MiNA remote/ubus）
调 `alarm.alarm_set_volume`，因此不依赖 SSH，也不需要刷机。
"""

from __future__ import annotations

from typing import Any

from homeassistant.components.number import NumberEntity, NumberMode
from homeassistant.config_entries import ConfigEntry
from homeassistant.const import PERCENTAGE
from homeassistant.core import HomeAssistant
from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.helpers.entity_platform import AddEntitiesCallback
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from .const import DOMAIN
from .coordinator import XiaoAiCoordinator


async def async_setup_entry(
    hass: HomeAssistant,
    entry: ConfigEntry,
    async_add_entities: AddEntitiesCallback,
) -> None:
    coordinator: XiaoAiCoordinator = hass.data[DOMAIN][entry.entry_id]
    async_add_entities(
        [XiaoAiAlarmVolumeNumber(coordinator, entry, sid) for sid in coordinator.speakers]
    )


class XiaoAiAlarmVolumeNumber(CoordinatorEntity[XiaoAiCoordinator], NumberEntity):
    """一台音箱一个闹钟音量滑块（0-100）。"""

    _attr_has_entity_name = True
    _attr_name = "闹钟音量"
    _attr_native_min_value = 0
    _attr_native_max_value = 100
    _attr_native_step = 1
    _attr_native_unit_of_measurement = PERCENTAGE
    _attr_mode = NumberMode.SLIDER
    _attr_icon = "mdi:alarm"

    def __init__(
        self, coordinator: XiaoAiCoordinator, entry: ConfigEntry, speaker_id: str
    ) -> None:
        super().__init__(coordinator)
        self._entry = entry
        self._speaker_id = speaker_id
        self._attr_unique_id = f"{entry.entry_id}_{speaker_id}_alarm_volume"

    @property
    def device_info(self) -> DeviceInfo:
        return DeviceInfo(
            identifiers={(DOMAIN, f"{self._entry.entry_id}_{self._speaker_id}")}
        )

    @property
    def _data(self) -> dict[str, Any]:
        return (self.coordinator.data or {}).get(self._speaker_id) or {}

    @property
    def available(self) -> bool:
        return (
            self.coordinator.speaker_available(self._speaker_id)
            and self._data.get("alarm_volume") is not None
        )

    @property
    def native_value(self) -> float | None:
        value = self._data.get("alarm_volume")
        return None if value is None else int(value)

    async def async_set_native_value(self, value: float) -> None:
        await self.coordinator.async_post(
            self._speaker_id, "/api/alarm_volume", {"value": int(round(value))}
        )
        await self.coordinator.async_refresh_fresh(self._speaker_id)
