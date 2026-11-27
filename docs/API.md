## HTTP API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/` | 内置播放器 WebUI（见上） |
| GET | `/healthz` | 存活 + `version`/`api` + 登录状态 + 播放器类型 |
| GET | `/api/state` | 队列视图（当前曲、位置、接下来 5 首、`loop`/`repeat`/`shuffle`/`liked`）+ 顶层 `volume`/`muted`/`poll`；有设备状态时附带 `device` 段 |
| GET | `/api/queue` | **完整队列**（`tracks[]` + 当前下标），WebUI / HA 浏览用 |
| POST | `/api/queue/remove` | 删除第 N 首：`{"index":5}`；`{"all":true}` = 清空并停止。播放中会重推整窗并 seek 回原进度，暂停中只记标记、等 `/api/resume` 再同步（**不会突然出声**） |
| POST | `/api/queue/move` | 重排：`{"from":8,"to":2}`（`to` 是移动后的下标）。当前曲身份不变，设备侧同步规则同上 |
| GET | `/api/device` | 设备真相 + 队列摘要（`probe` / `audio_id` / `position_ms` / `last_error`） |
| GET | `/api/search?q=&limit=&type=` | 搜索（不建队列）。`type=song`（默认，响应与旧版逐字节兼容）/ `playlist` / `artist` / `album` |
| GET | `/api/search/hot` | 热搜词（只读） |
| GET | `/api/recommend/playlists` | 推荐歌单：登录态个性化（`source=recommend`），未登录自动降级热门个性化（`source=personalized`） |
| GET | `/api/toplist` | 官方排行榜列表（65 个榜单，带封面）；榜单即特殊歌单 id，曲目用 `/api/playlist/{id}` 拉 |
| GET | `/api/newalbums` | 新碟上架（12 张） |
| GET | `/api/fm` | 私人 FM 一批曲目（只读；未登录/失败返回空列表，不报错） |
| GET | `/api/palette/{songId}` | 封面取色 `{vibrant,dark,muted}`（沉浸播放页背景）；下载自动加 `?param=400y400` 缩略，失败回默认蓝 + `fallback:true` |
| GET | `/api/playlist/{id}` | 拉歌单曲目（不建队列） |
| GET | `/api/daily` | 每日推荐曲目列表（只读，不触发播放） |
| GET | `/api/user/playlists` | 我的歌单列表（只读，WebUI 侧栏 / HA 浏览树用） |
| GET | `/api/account` | 账号信息 |
| POST | `/api/play` | 建队列并播放，body 五选一：`{"query"}` `{"playlist_id"}` `{"album_id"}` `{"song_ids":[]}` `{"daily":true}` |
| POST | `/api/enqueue` | 加歌但**不打断当前曲**：body 同 `/api/play`，`"next":true` 插到当前曲后面，否则加到队尾 |
| POST | `/api/hotmode` | **心动模式**：`{"seed":"current"\|"liked"\|<歌曲id>}`，拉一批推荐**替换**当前队列并从种子开始播；之后无限续杯（见下） |
| POST | `/api/hotmode/stop` | 退出心动模式（回到普通列表播放）；停止原因会写进 `/api/state` 的 `hotmode.stop_reason` |
| POST | `/api/jump` | 跳到队列第 N 首：`{"index":0}` |
| POST | `/api/next` `/api/prev` | 切歌 |
| POST | `/api/pause` `/api/resume` `/api/stop` | 传输控制 |
| POST | `/api/seek` | 拖进度：`{"position_ms":60000}`（设备 `player_set_positon`，单位毫秒） |
| POST | `/api/loop` | 旧接口：`{"mode":"list"}` / `single` / `shuffle` / `none`（`shuffle` 等价于 list + 随机） |
| POST | `/api/mode` | **播放模式（HA 用）**：`{"repeat":"off"\|"all"\|"one", "shuffle":true\|false}`，两个字段可分别省略 |
| POST | `/api/volume` | `{"value":30}` 或 `{"delta":5}` |
| POST | `/api/mute` | `{"value":true\|false}`，省略 `value` 则取反。设备无独立 mute 接口 → 音量置 0 + 记住原值 |
| GET | `/api/alarm_volume` | **闹钟音量**（0-100，云端 ubus）。与播放音量互不影响 |
| POST | `/api/alarm_volume` | 设闹钟音量：`{"value":40}` |
| POST | `/api/like` | 红心：`{"song_id":186016,"like":true}`；两个字段都可省略（默认当前曲 + 取反） |
| GET | `/api/events` | **SSE 实时流**：`event: state` / `event: lyric`，都是完整快照；`?speaker=living` 只要一台。HA 集成靠它，不再定时轮询 |
| GET | `/api/lyric` | 当前曲**正在唱的那一行**：`{"lyric":{"line","next_line","index","total","position_ms","song_id"}}`；`?full=1` 附上整首 `lines[]` |
| POST | `/api/login/cookie` | `{"cookie":"MUSIC_U=..."}` 手动登录 |
| GET | `/api/login/qr` | 返回二维码 PNG，`X-Unikey` 头带 unikey |
| GET | `/api/login/qr/check?key=` | 轮询扫码：800 过期 / 801 待扫 / 802 待确认 / 803 成功 |
| POST | `/api/logout` | **网易云登出**：清空持久化 Cookie 并清空红心缓存 |
| GET | `/api/admin/config` | **管理面**：读磁盘上的配置（凭据脱敏）+ `pending_changes`（已写盘但需重启才生效的项）+ `backups` + `runtime` |
| PUT | `/api/admin/config` | 合并写配置：从磁盘现值出发按字段覆盖，`Normalize` + 校验（未知键、重复 speaker id、非法枚举一律 400）；写前自动备份；响应给出 `changed` / `speakers_changed` / `requires_restart` |
| GET | `/api/admin/backups` | 列出当前配置文件的备份（新到旧） |
| POST | `/api/admin/config/restore` | 回滚到某份备份：`{"name":"config-20260101-120000.json"}` |
| POST | `/api/admin/restart` | **原地重启**（`syscall.Exec`，pid 不变、systemd 无感）：先取消 ctx 让各 session 落盘队列快照，再替换进程 |
| GET | `/api/admin/logs?n=200` | 最近的日志行（内存环形缓冲，最多 500 行） |
| GET | `/api/admin/auth/xiaomi/status` | 查询小米云端登录状态与绑定设备 ID |
| POST | `/api/admin/auth/xiaomi/logout` | 清除小米登录凭据 |
| POST | `/api/admin/auth/xiaomi/qr` | 发起小米账号扫码/链接授权（返回 `login_url` 与二维码 PNG base64） |
| GET | `/api/admin/auth/xiaomi/qr/check` | 轮询小米授权结果（`waiting` / `authorized` / `expired` / `failed`） |
| GET | `/api/admin/auth/xiaomi/devices` | 获取当前小米账号名下所有小爱音箱列表及在线状态 |
| POST | `/api/admin/auth/xiaomi/check` | 小米凭据体检（强制 passToken 续期并测试拉取设备） |
| GET | `/s/{songId}` | **设备取流入口**：现取网易云直链 → 302 / 代理 |

`/api/play` 也接受 query 参数写法（`?q=` / `?playlist_id=` / `?daily=1`），方便 HA 的 `rest_command` 直接拼 URL。

**管理面（`/api/admin/*`）**：写接口只改配置文件本身，**不做热更新**——音箱拓扑、`ssh_host`、
`media`、`listen`、认证文件路径这些在启动时就定下来了，保存后 `requires_restart=true`，
再调 `/api/admin/restart` 或 `systemctl restart` 才生效。写盘走 `Normalize` + 校验：
枚举值拼错、`speakers[].id` 重复（id 是 session key / 快照文件名 / HA entity_id 的来源）、
ssh 缺 `ssh_host`、音箱列表为空都会 400，且**校验失败不会动磁盘**。
敏感字段（`xiaomi.password` / `xiaomi.pass_token`）读出来只有掩码：PUT 里留空表示「没改」，
不是「清空」。整个 `/api/admin` 目前**没有鉴权**（内网可信是明确选择）——要让别的机器改不了，
就把 `listen` 绑到内网/loopback 或用反代限源。

**多音箱**：所有按音箱的端点都接受 `?speaker=<id>`（`/api/state`、`/api/queue`、
`/api/device`、`/api/play`、`/api/next`、`/api/pause`、`/api/volume`、`/api/jump`…）；
只有一台时可以省略。`GET /api/speakers` 返回所有 session 及其实时状态。
闹钟音量（`/api/alarm_volume`）同样接受 `?speaker=<id>`。
