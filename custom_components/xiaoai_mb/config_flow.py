from __future__ import annotations

import logging

import aiohttp
import voluptuous as vol
from homeassistant import config_entries
from homeassistant.core import HomeAssistant
from homeassistant.helpers import config_validation as cv
from homeassistant.helpers.aiohttp_client import async_get_clientsession

from .const import DOMAIN

_LOGGER = logging.getLogger(__name__)

DEFAULT_URL = "http://192.168.1.10:8088"


async def _fetch_speakers(hass: HomeAssistant, url: str) -> list[dict]:
    session = async_get_clientsession(hass)
    timeout = aiohttp.ClientTimeout(total=10)
    async with session.get(f"{url.rstrip('/')}/api/speakers", timeout=timeout) as resp:
        resp.raise_for_status()
        data = await resp.json()
    if not data.get("ok"):
        raise ValueError(data.get("error", "unknown error"))
    return data.get("speakers") or []


class XiaoAiConfigFlow(config_entries.ConfigFlow, domain=DOMAIN):
    """Configure one xiaoai-mb daemon and pick any subset of its speakers."""

    VERSION = 1

    def __init__(self) -> None:
        self._url = DEFAULT_URL
        self._speakers: list[dict] = []

    async def async_step_user(self, user_input=None):
        errors: dict[str, str] = {}

        if user_input is not None:
            url = user_input["url"].strip().rstrip("/")
            try:
                speakers = await _fetch_speakers(self.hass, url)
            except Exception:  # noqa: BLE001
                errors["base"] = "cannot_connect"
            else:
                if not speakers:
                    errors["base"] = "no_speakers"
                else:
                    self._url = url
                    self._speakers = speakers
                    return await self.async_step_speakers()

        return self.async_show_form(
            step_id="user",
            data_schema=vol.Schema({vol.Required("url", default=DEFAULT_URL): str}),
            errors=errors,
        )

    async def async_step_speakers(self, user_input=None):
        errors: dict[str, str] = {}
        options = {
            s["id"]: f"{s.get('name', s['id'])} ({s.get('control', '?')})"
            for s in self._speakers
        }

        if user_input is not None:
            chosen = list(user_input.get("speakers") or [])
            if not chosen:
                errors["base"] = "no_speakers"
            else:
                await self.async_set_unique_id(self._url)
                self._abort_if_unique_id_configured()
                names = {s["id"]: s.get("name", s["id"]) for s in self._speakers}
                return self.async_create_entry(
                    title=f"xiaoai-mb @ {self._url}",
                    data={
                        "url": self._url,
                        "speakers": chosen,
                        "speaker_names": {sid: names.get(sid, sid) for sid in chosen},
                    },
                )

        return self.async_show_form(
            step_id="speakers",
            data_schema=vol.Schema(
                {
                    vol.Required("speakers", default=list(options)): cv.multi_select(
                        options
                    )
                }
            ),
            errors=errors,
            description_placeholders={"count": str(len(options))},
        )
