# 媒体播放架构（小爱音箱）

> 回答四件事：**队列归谁 / 下一首谁定 / 直链怎么下发 / 状态怎么上报**，
> 并据此划出"把内容源换成自建服务"的改造边界。
>
> 证据标记：**【实】** = 二进制字符串、运行态 ubus 签名、现存可读脚本的实证；
> **【推】** = 按代码路径推断，尚未运行态验证（都汇总在 §9.3）。
> 功能/模块级清单见 `FEATURE-MAP.md` §3；本文只讲**数据流与控制流**。

---

## 0. 一句话结论

**播放队列与播放状态的真源在设备侧的 `mediaplayer` 进程，不在云端。**
云端只负责"内容源 + 曲库 + 直链"，设备自己拿着一张**会滚动补充的播放队列**。

推论（决定了改造空间）：

1. 设备能吃**任意 HTTP 直链**（原生 TTS 已经是这么播的）→ 换内容源不需要动播放器；
2. 按键、切歌、循环、进度都是**本地闭环**，断网照常工作；
3. 唯一必须由外部提供的是"**往队列里塞什么 URL**"——这件事可以整体搬到 NAS。

---

## 1. 拓扑

```
        内容源 / 曲库 / 直链                 队列窗口 / 索引 / 状态            解码出声
   api2.mina.mi.com (小米云)  ──┐
                                ├──►  mediaplayer  ──►  miplayer(VLC)  ──►  ALSA  ──► 喇叭
   任意 HTTP(S) URL（自建）    ──┘     （PlayList + 进度）      libffmpeg-miplayer
                                            ▲
                       ubus（本机 loopback） │
        ┌───────────────────┬───────────────┴────────┬──────────────────┐
     touchpad            mphelper              miio_helper          ha_agent
    （按键）           （shell 脚本）          （米家 MIoT）        （语音卫星）
```

- 所有控制入口最终都落到 **同一个 ubus 对象 `mediaplayer`**（§6）。
- 设备内部**没有**任何"第三方内容 SDK"：网易云/QQ/喜马拉雅只是云给出的 URL【实：`get_track_url play_url is %s`】。

---

## 2. 队列与"下一首"

| 环节 | 归属 | 证据 |
|---|---|---|
| 曲库、歌单、专辑、电台流 | 云 | `/device/playlist/v2/lists`、`/device/playlist/v2/songs`、`/device/station/sound_list`、`/device/audio/loadmore/v2`【实】 |
| 当前队列 `trackList_` / 当前索引 `curTrackIndex_` / 循环模式 | **`mediaplayer` 进程内存** | `PlayList::playIndex`、`track_list length is %d`、`LoopTrack/LoopSequence/LoopRandom`【实】 |
| 队列持久化（重启续播） | `/data/player/*.cfg`（`latest.cfg`、`player.cfg`） | `/data/player/player.cfg`、`stash_play_list`【实】 |
| "下一首放什么"（顺序/循环） | **本地** `curTrackIndex_ + 1` | `before next looping_type:%d,cur_track_index:%d`【实】 |
| 到列表尾部怎么办 | 向云要下一页（`loadmore`） | `needs_loadmore is %d`、`loadmore offset %d`【实】 |
| 电台/推荐流下一批内容 | **云**（按 `loadmore_token` 续） | `loadmore_token`、`needs_loadmore`、`matool_get_loadmorev2_list <type> <listId> <start> <count> <origin>`【实】 |

**结论：混合模式。** 顺序歌单的下一首本地决定；**无限流（电台/推荐）的下一批内容由云端决定**。
设备不是纯扬声器，它是一个"本地队列 + 云端续页"的边沿缓存。

---

## 3. 直链下发路径

```
云 API  ──►  play_url + auth(+token)  ──►  mediaplayer 拼 "?auth=" / "&auth="  ──►  VLC 拉流
```

- 拼接逻辑在 `get_track_url`：`&auth=`、`?auth=`、`&token=` 三种后缀【实】；
- 播放器实例：`miplayer_create()`（`libxiaomimediaplayerlite.so`，VLC 封装）+ `libffmpeg-miplayer.so`【实】；
- 设备当前声明的播放器方法里带 `auth`（Boolean）参数，说明"直链 + 鉴权串"是**一等公民**，
  不接受只有厂商才能生成的私有格式【实：`player_play_url{... auth:Boolean}`】。

---

## 4. 控制入口

### 4.1 机身按键（本地闭环，不经云）【实】

```
/dev/input/event0 → bin/touchpad（Dfa 组合键状态机）
   → libmico-common.so（player_toggle / volume_set / volume_get / player_mute_get_dec）
   → 由 pns 包装成 `/usr/bin/mphelper <verb>`（日志：`mipns-init.d: who=/bin/touchpad, cmd=… mphelper mu;…`）
   → ubus call mediaplayer player_play_operation '{"action":"…","media":"common"}'
```

- `touchpad` 的未定义符号只有 `player_toggle`/`volume_set`/`volume_get`/`touchpad_mute`/`alarm_is_ringing`；
- `mphelper` 是**可读的 shell 脚本**（`/usr/bin/mphelper`）→ **这是最容易插桩的控制点**。

**实测（2026-11-19，`xiaomi.wifispeaker.lx05` 实测）：机身只有 4 个动作，没有"上一首/下一首"。**

| 按键码 | touchpad 位 | 动作 | 实测结果 |
|---|---|---|---|
| 28 | bit0 | play/toggle | ✅ 暂停→`status:2`，再按→`status:1` |
| 114 | bit1 | mute | ✅ |
| 115 | bit2 | volume_up | ✅ |
| 139 | bit3 | volume_down | ✅ |

`touchpad` 的分发就是一个位掩码：`ldrb r3,[r4]; tst r3,#1 → "play "; tst r3,#2 → "mute "; …`，
只有这 4 个分支，字符串表里也没有 `next`/`prev`。所以按音量键只会调音量（会亮灯、会响提示音），
**不会切歌**——想切歌只能走 App / 语音 / 局域网 API（`player_play_operation next|prev`，实测有效）。

### 4.2 米家 App（两条候选）【推】

| 入口 | 形态 | 证据 |
|---|---|---|
| MIoT 属性/动作 | App → 米家云/局域网 → `miio_client` → `miio_helper` 的 `handle_get/set_properties`、`handle_action`（siid/aiid 分发） | `miio_helper`：`act_play_pause` / `act_play_next` / `act_play_prev` / `act_play_music` / `prop_playing-state` / `prop_audio-id` / `prop_volume`【实】 |
| 云端 ubus 桥 | App → 米家云 → MQTT → `messagingagent` 收到 `/internal/ubus`（`path`+`method`+`request_id`），执行后按 id 回结果 | `ubus request recieved:`、`Ubus request seccess request id:`【实】 |

倾向判断：**传输控制走 MIoT 动作，点歌/选歌单走 `/internal/ubus`**——因为 `handle_player_music`
的处理函数不带任何歌曲参数，而 `mediaplayer` 的 `player_play_music` 需要完整 JSON。
**这条必须用 `ubus monitor` 实测钉死（§9.3 第 1 项）。**

### 4.3 语音（历史 vs 现在）

- 历史：`mico_aivs_lab` 把 AIVS 的 `audio_player` 指令翻译成 `player_play_music` → 已随 aivs 移除（见 `VOICE-HA.md`）；
- 现在：`ha_agent` 只做 TTS/文本，**不产生点歌指令**；要恢复语音点歌必须由 HA 侧新造指令（§8.5）。

### 4.4 HA / 局域网（现役，已实证）

`ha_agent` 的 TTS 走通用单曲接口，**这是"外部程序往音箱塞 URL"的现成样板**：

```json
ubus call mediaplayer player_play_url \
  {"url":"http://<host>/x.mp3","type":4,"media":"mibrain","dialog_id":"...","duration":8000}
```

- `media` 是**通道**：`mibrain`（TTS）/ `common`（音乐）/ `bluetooth` / `linein` …；
  不同通道互斥且自动 stash/restore，所以 TTS 能压住音乐而不打断队列；
- `type=4` 是 mibrain-TTS 语义（`type=1` 走 beep 分支，会点 LED3）。

---

## 5. 状态上报与接收

### 5.1 内部（拉）

`player_get_play_status` / `player_play_status` / `player_get_context` / `player_get_latest_playlist`。

`player_get_play_status` 的回复是 **`{"code":0,"info":"{…}"}`**——真正的状态塞在 `info`
字符串里（`mphelper` 的 `mute_stat` 就是这么二次 `json_load` 的，别只读顶层）。
**细节还再套一层 `play_song_detail`**，实机 2026-11-19 抓到的完整形态：

```json
{"code":0,"info":"{ \"status\":1, \"volume\":19, \"loop_type\":2, \"media_type\":13,
  \"play_song_detail\":{ \"audio_id\":\"0\", \"position\":2026, \"duration\":29440,
                        \"global_id\":111111 },
  \"track_list\":[\"0\",\"0\"] }"}
```

| 字段 | 含义 | 备注 |
|---|---|---|
| `status` | 1=播放，0=停止，2=暂停 | 按键实测：暂停→2，恢复→1 |
| `loop_type` | 1=单曲，2=列表循环 | 内部枚举 |
| `media_type` | 13(d)=音乐 | **只有音乐才带 `play_song_detail`** |
| `play_song_detail.audio_id` | `player_play_url` 传的 `id` | `musics[]` 方式**恒为 `"0"`** |
| `play_song_detail.global_id` | `musics[]` 的 `global_id` | **int32 宽**，见下 |
| `play_song_detail.position/duration` | 毫秒 | 设备实际解出来的时长 |
| `track_list` | 队列里的 id 列表 | `musics[]` 方式全是 `"0"` |

⚠ **两个要命的细节（都是实测踩出来的）：**

1. `global_id` 实际按 **int32** 存：网易云新歌 id（如 `2657613721`）一律被钳成
   `2147483647`，多首歌会撞成同一个值，队列直接跟不住。
   → 这条分支因此必须用自建小整数。**我们已经不再走它**（见 §7.1.2，2026-11-20 迁到
   `payload` 分支，`audio_id` 是字符串、64 位无压力，devID 映射已删除）。
2. `player_play_url` 方式下 `global_id` 是 `0x7FFFFFFF` 哨兵、`audio_id` 才是我们的 id；
   而且 `media_type` 不是 13 时**整块 `play_song_detail` 都不给**（没有位置反馈）。
   → 想要位置反馈就必须走 `musics[]`（`player_play_music`）。

`musicd` 已按此实现（`musicd/sync.go`）：每 `status_poll` 秒拉一次，做位置纠偏、
发现外部切歌（`audio_id` 变了且落在自己队列里）、发现自然播完；拉到不认识的东西
（TTS / 别的通道）就让位。设备侧零改动。【实机验证：整窗下发 + 自动接歌 + 反向对齐全通】

> ⚠ 实机踩到的坑：`ssh root@… "ubus call …"` 的 stdout **前面带登录 banner**
> （`ROM Type:release / Ver:…`），直接反序列化必失败。要先截取第一个 `{` 到最后一个 `}`。
> 空闲时回复里**没有 `audio_id`/`position`/`duration`**，只有 `status/volume/loop_type`。

### 5.2 给 App / 云（三条）

| 上报 | 内容 | 去向 | 时机 | 强度 |
|---|---|---|---|---|
| MIoT 属性 | `playing-state`、`audio-id`、`volume`、`mute` | `miio_helper` → 米家云/App | App 查询或订阅后 `properties_changed`（`status_report_propertie`） | 【实】 |
| 跨端续播记录 | `alarmId`、`playCPId`、`playCP`、`playIndex`、`playPosition` | `matool_set_play_resource` → `PlayResourceAPI::setPlayResourceStatus` | 切歌/暂停/离开 | 【实】 |
| 播放埋点 | `switch_type`(Play/Pause/AutoSwitch/ManSwitch/MediaListSwitch)、`start_time`/`end_time` | `report_media_play_event` | 每次播放会话 | 【实】 |

### 5.3 已死的推送链

`mdplay_notify_*` / `mdclient_sync_*` / `miplay_*` 这几十个符号是"多房间/投屏/立体声"的状态广播，
目标是 `mdplay` / `miplay` 两个 daemon —— **本镜像里不存在，运行态 `ubus list` 也没有**【实】。
所以：多房间、立体声组网、投屏在这台机器上**从根上不可用**。

### 5.4 结论：App 进度条是"拉取 + 本地插值"【推】

设备侧**没有**"每秒主动推进度"的通道（唯一像的 `mdplay_notify_*` 是死链）。
App 拿到一次 `position/duration` 后自己走秒，状态变化时再取一次——
这解释了"大体同步但偶有偏差"的观感。

---

## 6. `mediaplayer` 接口清单（运行态实测签名）

```
player_play_url        url, type, group, media, src, dialog_id, id, instruction_id, duration, auth
player_play_music      music(JSON), startaudioid, startOffset, group, media, src, dialog_id, id,
                       instruction_id, duration
player_play_index      index, media
player_play_operation  media, action, message, grouping, dialog_id, instruction_id
player_play_commad     type, dialogid, messages        ← 原文拼写如此
player_play_album_playlist  type, id, startOffset, media
player_play_resource   message, src, volume
player_play_private_fm {}
player_play_alarm_reminder  id, type, reminder, volume, timeReminder, query
player_get_play_status {}      player_play_status {}      media_control_play_status {}
player_get_context {}          player_get_latest_playlist {}
player_set_positon     position, media                 ← 原文拼写如此
player_set_loop        type, media
player_set_volume      volume, media, beep, type, unit, tts_silence, lednum
player_set_continuous_volume  volume, media
player_modify_volume   isVolumeUp, value, type, unit, tts_silence
player_set_shutdown_timer  action, hour, minute, second, media
set_playled_status/action / media_control / test / player_reset / …
```

完整 40+ 方法签名见 `work_tmp/inv_ubus.txt`（`ubus -v list mediaplayer` 抓取）。

---

## 7. 播放指令 JSON（云 → 设备）

三套解析器：`parse_audio_player` / `parse_app_loadmore` / `parse_source_player`【实】。

下面 §7.1 / §7.2 的字段名、类型、嵌套层级是**静态反汇编出来**的（方法见 §9.1），
不是真机试错猜的——这三个二进制没剥日志字符串，每个 JSON key 都是 `.rodata` 里的
明文字面量，解析走的是 `json_object_object_get_ex(obj, "key", &out)`，字形固定，
所以纯静态就能把 schema 钉死。

### 7.1 `player_play_music` 的 `music` 参数

`mi_play_resource`（`mediaplayer` 0x254d8）把它当**普通字符串**取出，交给
`parse_audio_player`（0x29f50）现解析。三种写法**任一命中即可**，按以下顺序尝试：

**① `musics` 形式（最简，自建内容源用这个）**

```json
{
  "play_mode": "LoopSequence",
  "musics": [
    {"play_url": "http://192.168.1.122:8088/s/1234567", "global_id": 1234567},
    {"play_url": "http://192.168.1.122:8088/s/1234568", "global_id": 1234568}
  ]
}
```

| 键 | 类型 | 落点 | 说明 |
|---|---|---|---|
| `play_mode` | string | 循环模式 | `LoopTrack` 单曲 / `LoopSequence` 列表 / `LoopRandom` 随机；`LIST` 也认 |
| `musics[]` | array | 队列 | 每项一个音频条目 |
| `musics[].play_url` | string | item+0x188 | **原样拷进 1024 字节缓冲区，不校验、不拼 auth、无域名白名单** |
| `musics[].global_id` | **int32** | item+0x184 | 音频 id；**超过 2^31-1 会被钳成 `2147483647`**（实测），所以要用自建小整数 |

> 实测：`musics[]` 方式下发后，设备**自己按序播放**（播完自动下一首），
> 而且 `player_play_operation {"action":"next"}` 能正常切——这就是"队列留在设备侧"的形态。
> 但 `audio_id` 恒为 `"0"`（源码里就是 `strcpy(item+0x48,"0")`），此时 `startaudioid`
> **没法定位到某一首**；要指定起始曲只能把窗口旋转，让目标曲排第一。

走这条分支时 `audiotype` 被硬编码成 `0xd`（音乐），不需要另填。

**② `urllist` 形式**

```json
{"play_mode":"LoopSequence","urllist":[{"url":"http://…","audiotype":"MUSIC"}]}
```

`audiotype` 字符串 → 内部枚举：`MUSIC`→3、`NEWS`→8、`RADIO_STATION`→6、`BOOKS`→5、
`ANCIENT_POEM`→9、`WHITE_NOISE`→0xb、`VOICE`→0xe、`JOKE`→4、`TTS`→0x10，缺省 0xd。

**③ AIVS 标准 `payload` 形式**（`mico_aivs_lab` 自己用的就是这套）

```json
{"payload":{
   "play_behavior":"…",
   "audio_items":[{"item_id":{"audio_id":…,"cp":…,"name":…,"id":…,"album_id":…,
                              "episode_index":…},
                   "stream":{"url":"…"},
                   "authentication":{"token":"…"},
                   "offset_in_ms":0,"duration_in_ms":0,"redirect":…,"item_type":…,
                   "play_times":…,"skip_controls":{"NEXT":…,"PREV":…},"log_extension":"…"}]
}}
```

**三者都没有** → 日志 `parse_audio_player failed by no payload field`，返回码 `0x87`。

**C. App 续页**（另一条解析器）：`{ "data": [...], "nextStartOffset": N }`。

### 7.1.1 米家原版 payload 实测（2026-11-20，抓自 `messagingagent` 日志）

趁人在 App 里点播一首，把云端真正下发的报文原样抓了下来——**这是判断"能不能冒充米家"
的唯一硬证据**：

```json
{"payload":{"audio_items":[
  {"item_id":{"audio_id":"2965821255529268103",
              "cp":{"album_id":"3559706031156103781","episode_index":0,
                    "id":"734142DCC1B0117AC6F1B984E3706EB5","name":"wangyiyun"}},
   "offset":0,
   "stream":{"authentication":true,"duration_in_ms":213000,"offset_in_ms":0,"redirect":false,
             "url":"https://resource.ai.xiaomi.com/cp_resource_locator/c/v3/aibox_content_internal?audioId=2965821255529268103"}}]}}
```

由此确认五件事：

1. **`audio_id` 是 64 位**（`3.0e18` 量级），完全不是 int32。`int32` 钳位只发生在
   `musics[].global_id` 那条旁路（§7.1 ①），不是设备的普遍限制；
2. **这些 id 是小米全局顺序分配的内部 id**：实测同一列表里出现
   `…7477712498 / …499 / …500` 三个**连续** id（差 1），与网易云 id（~1e9）没有换算关系；
3. **`cp.id` 每首歌都不同**（`734142…` / `C5F74B…`），不是固定的内容商常量；
4. **payload 里没有任何歌名/歌手/封面字段**（`title|song_name|album_name|artist` 零命中）——
   设备拿到的只是"不透明 id + 一个要授权的 URL"；
5. `stream.url` 指向 `resource.ai.xiaomi.com`，**设备自己往后面拼授权**：

   ```
   PlayList::set url:…?audioId=…&auth=MIOT-TOKEN-V1%20app_id%3A…%2Csession_id%3A…%2Ctoken%3A…%2Cdevice_id%3A…
   ```

   解码即 `MIOT-TOKEN-V1 app_id:…,session_id:…,token:…,device_id:…`（AGENTS.md 记的那套本地拼装）。

**从 NAS 直接 curl 那个地址（不带 auth）：**

```json
HTTP 401 {"status":{"code":401,"error_type":"unauthorized","error_details":"Check ai auth parameters."}}
```

#### 结论：为什么我们只能用自建直链，以及为什么"上报 cp"没意义

- **自建直链的真正原因不是 id 上限，而是授权**。小米的音频流要小米签发的 auth，
  我们不是签约内容方，拿不到；所以只能把 `stream.url` 指向自己的服务器，
  走 `musics[]` / `play_url` 那条"自建内容旁路"（原样使用、不拼 auth、无域名白名单）。
- **`cp` 上报在这条旁路上根本不存在**。实测日志：

  ```
  report_media_play_event, payload is :{ … "audio_id": "0", "cp_id": "", "cp_name": "",
                                          "cp_album_id": "", "episode_index": 186016, … }
  ```

  `musics[]` 分支把 `audio_id` 硬编码成 `"0"`、`cp_*` 全空，我们在条目里塞的
  `cp`/`audio_id` 被直接丢弃，只有 `global_id` 落进了 `episode_index`。
- **就算能填也没用**：歌名/封面是 App 拿 `audio_id` + `cp.name` 去小米云端**反查**的，
  我们的 audio_id 不在小米库里 → 永远反查不到。
- 唯一能"变好看"的路是把网易云歌解析成小米 `audio_id` 再下发原生 payload
  （设备会自己拼 auth）。但解析接口在 App/云端（设备日志里只有 `device/presence` 和
  `device` 两个 mina 接口），且可能受小米音乐会员约束，而且等于改用小米的内容服务，
  与"自建、不依赖小米生态"的目标相反。**已放弃，见 FEATURE-MAP §16.2。**

### 7.1.2 AIVS `payload` 分支实测：**可用**，关键字段是 `payload.audio_type`

> ✅ **2026-11-20 已迁移**：整窗下发从 `musics[]` 改走本节所述的 `payload` 分支，
> `devID` 映射表与 `/d/<devID>` 入口已删除（URL 改为 `/s/<songID>`）。

一开始我们以为 payload 分支"能播但不记账"，这个结论是**错的**（2026-11-20 晚已推翻并查清）。
米家 App 走的正是 payload 分支，它的 `player_get_context` 能给出完整 `audio_meta`
（`cp.name` / `album_id` / `favorites_id` / `position` / `track_list`），
`miio_helper` 每 60 秒上报、App 前台时云端每 5 秒拉取的就是这份东西。

**差的就是一个字段**：`audio_type` 必须是 **payload 顶层的字符串 `"MUSIC"`**。
不给（或给错位置）就会退化成 `TTS`，于是设备不做音乐记账：
`play_song_detail` / `track_list` / `audio_meta` 全空。

反汇编 `parse_audio_player`（0x29f50）得到的 payload 分支字段顺序：

```
payload
  play_behavior → loadmore_token → loadmore_delta → audio_type → needs_loadmore
  → favorites_id → origin_id → play_duration → list_params{type, listId}
  → origin → loadmore_offset → stop_by_count → play_position → stop_by_time
  → audio_items[{ item_id{audio_id, cp{name,id,album_id,episode_index}},
                  offset, item_type, stream{url, authentication, duration_in_ms,
                                            offset_in_ms, redirect} }]
```

`audio_type` 是**字符串**，按 `MUSIC` / `NEWS` / `RADIO_STATION` / `BOOKS` / `ANCIENT_POEM`
/ `WHITE_NOISE` / `VOICE` / `JOKE` / `TRANSLATION` / `TTS` 依次比较，命中后映射成内部枚举
（日志 `audio_type is 3` 即 MUSIC）。

#### 实测对比（同一首歌、同一 URL）

| 回读项 | `musics[]` | `payload` + `audio_type:"MUSIC"` |
|---|---|---|
| `media_type` | 13 | **3** |
| `play_song_detail` | `{audio_id:"0", position, duration, global_id:1158}` | `{audio_id:"469391964", position, duration}` |
| 身份字段 | `global_id`（int32，需 devID） | **`audio_id`（64 位字符串，网易云原 id）** |
| `track_list` | `["0"]` | **`["469391964","469391965","469391966"]`** |
| `player_get_context.audio_meta` | 无 | **`{audio_id, cp{name,id,album_id}, favorites_id, audio_type:"MUSIC"}`** |
| 埋点 `cp_id`/`cp_name` | 空 | **有值** |
| 位置/时长 | ✅ | ✅ |

也就是说 **payload 分支能完整替代 `musics[]`，而且天然支持 64 位 id、能上报 cp**。

#### 列表语义（实测，迁移必读）

| 行为 | `musics[]`（`play_mode: LoopSequence`） | `payload`（`needs_loadmore:0`） |
|---|---|---|
| 批内自动下一首 | ✅ | ✅（`audio_id` 逐条推进） |
| 列表播完 | 绕回窗口开头继续放 | **停止**（`status=0`） |
| `player_set_loop type=1` | — | **绕回第一条**（列表循环） |
| `player_set_loop type=2` | — | 不循环（仍停） |

所以迁移后循环模式映射：列表循环/随机 → `type=1`；单曲循环 → 窗口只放 1 首 + `type=1`；
不循环 → `type=0` 播完即停（比现在靠"窗口绕回队首"探测更干净）。

#### `play_behavior: "APPEND"` 的真实语义【实】

设备支持 `APPEND`（`code:0`），而且 `track_list` **确实会变长**——但会**立刻把播放位置
跳到新追加的第一首**，当前曲被掐断：

| 操作 | `track_list` | 当前曲 |
|---|---|---|
| `REPLACE_ALL [A,B]`，放到 7.5s | `[A,B]` | A @7.5s |
| 再 `APPEND [C,D]` | `[A,B,C,D]` | **C @2.9s**（A 被掐断） |

改 `offset`（续号 / 从 0 起）、加 `needs_loadmore` / `loadmore_offset` / `loadmore_delta` /
`stop_by_count` 都一样跳。这是给云端 loadmore 用的语义（一批放完才追加，跳到新批正好是
自然衔接），**不能用来"不打断当前曲地加歌"**。

另一个实测坑：`player_play_music` 的 `music` 参数必须是 **`{"payload":{…}}` 整包**
（外层再套 `{"music":"<字符串>","media":"common"}`）。少 `payload` 这层会
`parse_audio_player failed by no payload field`，ubus 回 `code:135`。

> 小米自己也是这个模型（2026-11-20 用 141 首歌单实测）：
> 云端**一次只推 20 条**（`items_len=20`），设备 `track_list` 也就 20 条——**不缓存全量**；
> 播到批末尾时按 `loadmore_offset` 主动向云端要下一批，日志原文：
>
> ```
> get_loadmore_list,command:matool_get_loadmorev2_list PLAYLIST <歌单id> 20 5 wangyiyun
> ```
>
> 即"偏移 20、数量 5"，拉到后 `track_list` 从 20 涨到 25。
> （另一次 15 条的歌单也是先推 15 + `needs_loadmore:1`。）
>
> 播到批末尾再按 `loadmore_offset=15` 向云端要下一批（`matool_get_loadmorev2_list`）。
> **批内每条自带 `stream.url`，但那是长效 locator**（`resource.ai.xiaomi.com/cp_resource_locator/?audioId=`），
> 每次起播才拼 auth 解析成带签名的 CDN 地址（实测日志有 8 次 `PlayList::set url:https://resource.ai...&auth=`）。
> 所以"云端分批推"能扛过 20 分钟，是因为 URL 是 locator 而不是直链——**我们的 `/s/<songID>` 正是同款设计**。
>
> 反过来说：**如果我们也能做到"到点再推"，直链确实可行**（每批都是新签名的 URL）。
> 但我们没有 loadmore 钩子（它走 `matool` 直连小米云），只能靠轮询发现"这批放完了"——
> 那时快则 15 秒、慢则 60 秒，中间是静音空档。与其换来这个空档，不如保留长效 locator。

控制路径同样兼容（实测）：payload 建立的列表用 `media=common` 调
`player_play_operation{pause|play|next|prev}` 全部生效，`play_song_detail.audio_id`
随切歌正确变化（`469391964 → 469391965 → 469391964`）。
所以"迁移到 payload"在技术上没有阻塞，收益是**可以彻底删掉 devID 映射**（已于 2026-11-20 落地：
（`global_id` 的 int32 限制只存在于 `musics[]` 分支），现在走 `/s/<songID>` + `audio_id`）。

#### 但"直链直接下发"仍然不行

payload 解决的是"分支选择"，不解决"URL 有效期"：网易云直链 `expi=1200s`（实测），
签名嵌在路径里不可延长：

```
http://m702.music.126.net/20261120223305/<32字节签名>/...mp3
                            ↑ 签发时刻 YYYYMMDDHHMMSS
```

所以 `stream.url` 仍应指向**我们自己的稳定地址**（`/s/<songID>`），
由它在设备真正请求的那一刻去换直链。50 首窗口（约 3.5 小时）配 20 分钟 URL 是不可能的。

> 之前得出"payload 不记账"这个错误结论，原因是把 `audio_type` 塞进了 `audio_item`
> 而不是 payload 顶层（还试过数字、`audiotype` 等各种变体，都不是字符串 `"MUSIC"` 的正确位置）。
> 最终可用的 `item_id` 只需要 `{audio_id, cp{name,id,album_id,episode_index}}`，
> 不需要 `name` 字段。

### 7.2 `player_play_url` 的 `type`### 7.2 `player_play_url` 的 `type`

`mi_play_url`（0x25a88）对 `type` 只做一次钳位：**`type >= 0x14` 一律当 1**。
小米自己的 `mico_aivs_lab` 发音乐/普通 URL 时填 **`type=1`**；HA TTS 走的是
`TTSUbusHandle`，填 `type=4`（`media=mibrain`）。URL 同样原样交给播放内核，
**没有域名白名单、没有强制签名**——这是"自建内容源"能成立的根。

---

## 8. 换成自建内容源：分工设计

### 8.1 硬约束

| 约束 | 数值/事实 | 影响 |
|---|---|---|
| RAM | 空闲仅 **1.6–2.4MB / 58MB**（§14.2） | **不要新增常驻进程** |
| CPU | Cortex-A7 单核 | 不做转码、不做 DSP |
| 运行时 | 无 python/perl/jq；有 `curl`、`uclient-fetch`、`jsonfilter`（busybox 系） | 逻辑尽量放 NAS |
| 已有常驻件 | `ha_agent`（140KB 二进制，已含 HTTP 客户端 + Wyoming 长连接） | 需要回传通道时**扩展它，不新增进程** |
| 播放器能力 | VLC 内核，http/https/HLS 均可 | 只认 URL，不关心来源 |

### 8.2 分工表

| 职责 | 放哪 | 理由 |
|---|---|---|
| 网易云登录 / cookie 续期 | NAS | 需浏览器扫码，设备做不了 |
| 搜索、歌单、每日推荐、私人 FM | NAS | 需要完整 API |
| **队列（队列真源）** | NAS（`musicd`） | 推荐/去重/插队只有 NAS 算得动 |
| 直链解析 + **代理**（`/s/<songid>` → 302 到新鲜直链） | NAS | 解决 URL 过期、TLS、VIP 重试 |
| URL 窗口下发 | NAS → 设备（ubus / ssh） | 设备只负责播 |
| 播放、解码、音量、ducking、LED | 设备 `mediaplayer` | 原生能力，别重造 |
| 按键 / App 状态显示 | 设备原生 | 免费得到 |
| 语音意图（"放周杰伦"） | HA + `musicd` | 已有 Wyoming/Assist 管线 |

### 8.3 三种驱动模式

| 模式 | 做法 | 按键 next/prev | 进度条 | 实现量 |
|---|---|---|---|---|
| **D 哑终端（一轨一推）** | NAS 决定下一首 → 推单曲 URL | **失效**（队列只有 1 首） | 单曲正确 | 最小 |
| **B 窗口推送（推荐）** | NAS 一次推 N 首（含 url+auth+duration+audio_id）→ 设备原生切歌，剩 <5 首时补推 | **原生可用** | 正确 | 中 |
| **S 直播流（MPD httpd）** | NAS 上 `mpv`/`mpd` 播，`httpd` 输出一个永久 URL，设备循环播它 | 失效（需回传） | 无意义 | 中，但设备侧最省 |

- **D 的补丁**：`mphelper` 是 shell → 可改成"先问 NAS 拿下一首，再 `player_play_url`"，按键就复用了；
- **S 的补丁**：MPD 的 `httpd` output 天然是"永不失效的直链"，
  本质是把设备降级为收音机——**适合"电台模式"，不适合"专辑/歌单模式"**。

### 8.4 推荐形态：B + 代理直链 + `musicd`

```
[手机 App / 语音 / 网页]
        │  HTTP
        ▼
  NAS: musicd ──► 网易云 API（cookie）──► 直链
        │  ①窗口(20 首) ②/next ③/state
        ▼
  设备: mediaplayer（原生队列、原生按键、原生 App 状态）
        ▲
        └── 播放进度/切歌事件回传（ha_agent 控制通道 或 mphelper→curl NAS）
```

要点：

1. **URL 用代理形式**（`http://nas:8080/s/12345`），设备侧永不失效，NAS 每次 302 到新鲜直链；
2. `audio_id` 填网易云 song id → App 的 `audio-id` 属性有真实值，且 `track_list_find` 去重正常；
3. `cp`/`cpId` 留空或填自定义命名空间 → 避免触发 `matool_get_play_resource` 的云端队列表；
4. **窗口补推**：剩 5 首时整表替换 + `player_play_index` 回到当前曲，比 `append_music_list` 更好验证；
5. 米家 App 的暂停/下一首**继续原生可用**，不冲突（它们操作的是本地队列）。
6. **服务端已落地，且有两种 transport**：`musicd/`（Go，单文件）。
   - `ssh`：定制机，`ssh → ubus`（已实测整窗下发/自动接歌/状态回读）；
   - `cloud`：原厂机，小米 MiNA `remote/ubus` 代理（**不需要 SSH、不需要刷机**，已实测）；
   - 直链对外统一用 `/s/<songID>`（现取现跳，绕开 20 分钟过期）；
   - **一个 daemon 可同时驱动多台**：每台一个独立 session（队列/探针/音量），
     按音箱的 API 用 `?speaker=<id>` 选择；
   - 另有内置 WebUI（`http://<nas>:8088/`，顶部可切换音箱）和 HA 集成
     （`custom_components/xiaoai_mb`，一个入口多选音箱、每台一个设备）。
   设备侧始终零改动。配置与 API 见 `musicd/README.md`。

### 8.4.1 播放模式（2026-11-20 落地）

播放模式被拆成两个**正交**维度，落在每个 session 的队列上：

| 维度 | 取值 | 含义 | 设备侧表现 |
|---|---|---|---|
| `repeat` | `none` | 播到队尾就停 | 窗口在队尾截断；设备 LoopSequence 绕回窗口头时 **检测到并停止** |
| | `list` | 列表循环（默认） | 窗口首尾相接，设备自己循环 |
| | `single` | 单曲循环 | **窗口只下发当前这一首**，设备循环它 = 原地重放 |
| `shuffle` | `true/false` | 随机 | 窗口顺序整体洗牌（首曲固定当前曲），回读 `global_id` 对齐队列下标 |

- API：`POST /api/mode {"repeat":"off|all|one","shuffle":bool}`（HA 用，两字段可分别省略）；
  旧接口 `POST /api/loop {"mode":"none|list|single|shuffle"}` 保留，`shuffle` 等价于 list+随机；
- `GET /api/state|queue` 同时给 `loop`（展示标签，开随机时报 `"shuffle"`）和
  `repeat`（真实循环语义）两个字段，老 WebUI 不用改；
- HA 映射：`repeat off/all/one` → `none/list/single`，`shuffle` 直接透传；
- 随机 + 列表循环时，手动下一首会**本地移动后重推整窗**（设备窗口里的下一首不一定是队列的下一首）。

### 8.4.2 扩展操作：进度跳转 / 静音 / 入队 / 红心 / 歌词

设备侧与网易云侧的事实（都是实机验证）：

| 能力 | 走哪条接口 | 实测结论 |
|---|---|---|
| 拖进度 | ubus `player_set_positon {"position":<ms>,"media":…}` | ✅ 60000 → 设备报 61913 |
| 静音 | 设备没有独立 mute 接口 | 用 `player_set_volume 0` + 服务端记住原值。台账 §5.1 里的 `set_player_quiet{quiet}` 语义没验证（更像“安静模式”，会连提示音/播报一起管），不敢拿来做播放静音 |
| 整窗续播 | `player_play_music` 的 `startOffset` | ❌ **被忽略**（45000 / 45 都无效，一律从 0 开始） |
| 重推后 seek | `player_set_positon` 紧跟 `player_play_music` | ⚠ 会被**静默忽略**（ubus 照样回 code 0），所以要回读位置、没到位就重试（`seekVerified`） |
| 收藏 | 网易云 `/api/radio/like`（`os=pc` cookie） | ✅ 加/取消都生效；`/weapi/song/like/get` 拿全量喜欢列表（2 分钟 TTL，异步刷新） |
| 歌词 | 网易云 `/api/song/lyric`（linuxapi） | ✅ LRC 解析；一行多时间戳、逗号毫秒都吃；无词的歌安静返回空 |

为什么要做成独立实体：

- **红心**：`MediaPlayerEntityFeature` 里根本没有 LIKE 这一位，HA 原生播放器卡片
  也长不出这个按钮。做 `switch` 而不是 `button` 是因为要显示“实心/空心”这种状态；
  `is_on` 纯粹是 `musicd` 返回值的一个投影，本地不做乐观更新，操作完立刻
  `async_refresh_fresh`，所以不存在两处状态互相拉扯。
- **歌词**：`media_player` 没有放歌词字段的地方。用 `sensor`（只读）而不是
  `text`（那是“可写输入框”语义），state 就是当前这一行。时间轴在 `musicd` 算
  （它本来就有进度，探针每 `status_poll` 秒纠偏），HA 每 3 秒取一次当前行，
  不参与时间推算，避免两边时钟漂移。
- **入队不能打断当前曲**：所以 `/api/enqueue` 在整窗模式下是
  “改队列 → 重推整窗 → 带确认的 seek 补回位置”。

### 8.4.3 原厂同步策略实测（2026-11-20，客厅机 + 米家 app 实机操作）

结论先行：**原厂没有推送，只有拉**；而且拉分两层，节奏差别很大。

| 观测手段 | 观测结果 | 说明 |
|---|---|---|
| `ubus listen`（含 8 次机身按键 + 米家 暂停/播放/暂停 的 4 分钟窗口） | **0 条消息** | 设备内部不广播播放事件，本地订不到 |
| `tcpdump` 抓公网流量（机身 `player_play_operation` 触发时） | 只有 15~40s 一次、53 字节的心跳 | 状态变化**不会**触发设备主动上报 |
| `messagingagent` 日志（`HTTPRequestToDevice.cpp:621 ubus request parsed`） | **每 5.0~6.0 秒一次 `player_get_play_status`** | 这就是云端的拉：app 在前台才拉，退到后台就停（实测出现过 57s / 517s 空档） |
| `miio_helper` 日志（`pipe (ubus -t 1 call mediaplayer player_get_context)`） | **每 60 秒一次**，与 app 在前后台无关 | 属性快照上报：`audio_meta`（`cp.name="wangyiyun"`、`cp.id`、`album_id`、`favorites_id`）、`position`/`duration`、**`track_list`（整条队列的 audio_id 列表）** |
| 云侧下发过的方法统计（全天） | `player_get_play_status` ×77、`player_play_operation` ×5、`player_play_music` ×1 | 查询远多于控制 |
| `/tmp/stat_points_rom.log`（埋点；本固件里 `statpoints_daemon` 没运行 → 只增不减） | 机身键写 `mediaplayer_action=toggle`（通道 `common`）+ `mediaplayer_pause=1`；米家写 `mediaplayer_action=pause`（通道 `app_android`）；音量 `mediaplayer_setvolume=19`；麦克风 `micolog_mic_off_count` | **本地留痕，实测 `tail -F` 1 秒内可见** —— 以后给 ssh 机做"事件驱动"的入口 |

为什么米家显示得比我们"准"：

1. **你在 app 里按的键，app 自己先画**（乐观更新）；机身上按的键靠那个 5 秒拉；
2. **内容/进度/列表不是设备给的文本，而是云端拿 `audio_id` 反查自己的内容库**。
   我们用的是自建小整数 id（`/d/1000`，为了绕开 `global_id` 的 int32 钳位），
   小米库里查不到 → 所以米家能显示"播放中/暂停"（`status` 是设备直接给的），
   却永远显示不出我们在放什么、放到哪、列表是什么；
3. 你看到"重开 app 时内容变成了实际播放的那首"= 那份 **60 秒一次的属性快照**
   被刷新了，app 冷启动读的是云侧缓存。

### 8.4.4 我们的回读节奏：稳态 + 不稳定态（`musicd/poll.go`）

```
稳态     status_poll   ssh 15s / cloud 60s        （没人动它就一直躺在这）
   │  ① 我们发了任何指令（play/pause/next/jump/seek/mode/volume/mute/enqueue/stop）
   │  ② 设备换了歌（回读发现 track_id 不是我们预期的那首）
   │  ③ 播放态与预期不符（我们以为在播，设备报暂停，反之亦然）
   │  ④ 设备比我们更靠前 > poll_drift_ms（ssh 4s / cloud 8s）
   │  ⑤ 探测从失败恢复（掉线这段时间里状态可能已经变了）
   ▼
不稳定态 poll_burst × poll_rounds = 5s × 3        （重复触发只重置余量，不叠加）
   │  跑完 3 轮
   ▼
回到稳态
```

- **进度不会被拉长的间隔拖住**：两次回读之间靠本地时钟插值
  （`Queue.SyncPosition` 锚定 + `PositionMs()` 推算），和 app 的做法一样；
- **一个必须一起修的实现坑**：调度器原来每 500ms 拿"缓存的设备状态"去 `SyncPosition`。
  稳态 5s 时看不出问题，稳态拉到 60s 就会把进度**钉死在旧值上再跳一下**。
  现在 `deviceTracker` 带数据代次（`generation`），**只有新数据才同步**；
- **云端偏差阈值为什么是 8s**：MiNA 从下发到真正开播有 2~5 秒延迟，阈值留 4s 会在
  起播那阵子反复误判成"不可靠"，burst 就永远停不下来；
- 实测（客厅机稳态 15s）：起播后 `poll={mode:"burst", reason:"整窗下发"}`，15 秒后回稳态；
  在设备上外部 `player_set_positon` 跳到 200s（比我们靠前 101.9 秒）→ 日志
  `⚡ 状态不可靠（设备比我们靠前 101920ms）→ 5s×3 加密回读`，下一轮就锚定回真值；
  外部暂停 → `⚡ 状态不可靠（播放态与预期不符）`，两类都在下一轮纠正；
- **偏差判定是单向的**（只看"设备更靠前"）：反过来"我们的估算更靠前"绝大多数是
  单曲循环 / 窗口绕回把设备位置重置成 0，`SyncPosition` 安静重锚即可。
  之前用绝对值判定时，卧室机每循环一次就会打一条 `位置偏差 60000ms` 的空转 burst；
- 实测（卧室机稳态 60s）：起播只触发一次 burst（`整窗下发`），之后 `mode:"steady"`，
  进度靠插值连续推进（20 秒内 10655ms → 30780ms），**没有**因为间隔变长而冻结或回跳；
  阈值还是 4s 时起播阶段会被误判成"位置偏差 5500ms"反复进 burst，这就是云端放宽到 8s 的原因；
- **为什么不用无脑高频**：原厂云→设备是跨公网且只能由云发起，它必须门控；
  我们 NAS→设备是局域网内 ssh/MiNA，稳态放宽到 15s/60s、把密度花在真正需要跟手的时刻，
  **外部变更的反应时间不输原厂，而 MiNA 的总请求数比原来固定 45s 还低**。

> ⚠ `/tmp/stat_points_rom.log` 在 tmpfs（RAM）里，而本固件没有消费者
> （`statpoints_daemon` 没运行），所以它会一直长。现在量级是 KB；
> 真要用它做事件流，得同时决定轮转策略（见 FEATURE-MAP §16.2）。

### 8.4.5 HA ↔ musicd：SSE 事件流（`musicd/events.go` + HA coordinator）

设备侧拉不动得更勤（MiNA 反滥用 + 64MB 设备），但 **NAS→HA 这一跳没有任何理由等 60 秒**，
于是把"谁是权威"这件事彻底摆正：**musicd 是唯一权威状态源，它推，HA 消费**。

- 端点：`GET /api/events`（SSE，`text/event-stream`，带 `X-Accel-Buffering: no` 穿透 nginx）；
  可选 `?speaker=living` 只要某一台；
- 两种帧，都是**完整快照**（不做 diff），丢一帧也不会错：
  ```
  event: state   data: {"ok":true,"speaker":"living","queue":{...},"device":{...},"volume":19,"muted":false,"poll":{...}}
  event: lyric   data: {"ok":true,"speaker":"living","lyric":{"line":"...","index":5,...}}
  ```
- **判定"变化"的字段**（`stateSig`）：当前曲 id、队列下标/总数、playing、liked、
  repeat/shuffle、source、音量、muted、设备 status/media_type、在线与否与错误文本。
  **位置不参与**——它每秒都在变，推它就等于每秒写一次 HA 历史；
  进度仍由 HA 按 `media_position` + `media_position_updated_at` 本地插值；
- 发送点复用调度循环那一个 500ms tick（不新增定时器），签名没变就零开销；
  订阅者队列长度 8，慢消费者**丢帧不阻塞**调度循环；
- 心跳 `: ping` 每 20 秒一次（防反代/中间设备掐连接）；
- HA 端：`XiaoAiCoordinator._stream_loop()` 读流 → `_apply_event` 打进 `self.data[speaker]` →
  `async_set_updated_data`；流在线时 `_async_update_data` **一个 HTTP 都不发**；
  流断了指数退避重连（2→30s），期间自动退回 `DEFAULT_SCAN_INTERVAL` 轮询（并顺手取歌词），
  功能不瘸，只是新鲜度降级；
- 歌词实体（`sensor`）因此也变成纯投影：不再有每 3 秒的自轮询，行号变化由 lyric 帧驱动；
  `manifest.json` 的 `iot_class` 相应改为 `local_push`。

实测（两台都 60s 稳态 + SSE）：

| 场景 | 结果 |
|---|---|
| HA 里点"下一首" | **1.33 秒** 后 HA 实体 `media_title` 变了（以前最坏 60s） |
| 歌词 | 每行变化 ~5-6 秒一次随帧到达，HA 侧零轮询（日志里不再有周期性 `/api/state`、`/api/lyric`） |
| 机身/米家的外部暂停（持续 36 秒） | **被完全漏掉**：两次稳态探测（间隔 60s）之间开始并结束，HA 状态从未翻转；位置后来靠下一帧跳回真值自纠正 |

最后一条是 pull 模型的**混叠（aliasing）**固有代价：采样周期比事件持续时间长，事件就会被跳过。
原厂之所以不受影响，是因为 app 在前台时云以 5 秒采样。所以要么把 ssh 机的稳态间隔压到
远小于"用户可能按住暂停的时间"，要么接上本地事件流（§8.4.3 的埋点文件），后者是唯一根治办法。

### 8.4.6 心动模式（`musicd` + HA 媒体浏览器入口，2026-11-20 落地）

「心动模式」= 拿一首歌当种子，让网易云的推荐算法算一批歌。接口是
`/weapi/playmode/intelligence/list`（`songId` = `startMusicId` = 种子，
`playlistId` = 「我喜欢的音乐」的 id，`type=fromPlayOne`）。

| 入口 | 种子 | 行为 |
|---|---|---|
| **心动·当前**（HA 媒体浏览器 `hotmode:current`） | 正在放的那首 | 种子排第一 + **进度拉回原处**，先把手头这首放完再过渡到推荐 |
| **心动·红心**（HA 媒体浏览器 `hotmode:liked`） | 「我喜欢的音乐」随机一首（排除正在放的） | 从这首开始放，然后接推荐 |
| `POST /api/hotmode {"seed":"<id>"}` | 指定歌曲 | 同上 |

实现要点（都是实测踩出来的）：

- 库里对口的 `PlaymodeIntelligenceListService` **有 bug**：`data["count"]` 从未赋值，
  永远发 `"1"`，只拿回 1 首 → 改成直接发原始请求；
- **服务端会忽略 `count`**：要 30 给 118 条，所以由我们按 `window` 截断（默认 50）；
- 响应是 `data[].songInfo`（不是直接歌曲对象），`songInfo.privilege.st < 0` 是无版权，
  先按它粗筛一层；
- **VIP/版权会真的放不出来**：实测周杰伦原版《晴天》(186016) 是 VIP 独占，
  `SongURL` 直接 404，整窗把它排第一的结果是设备 404 跳过、播报"播放失败"。
  所以种子与整批推荐都**并发验一次可播性**（5 并发 `resolve`，51 首约 1.2s）后剔除，
  副作用是直链缓存被预热、起播更快；
- **种子先验证**：`liked` 随机试最多 10 首直到挑到能放的；`<id>` 放不了直接 409；
  `current` 若发现这首其实放不了就退化成"仅推荐"；
- **强制 `LoopList` + 关闭 shuffle**：推荐顺序是算法给的，随机化就没意义；
  而且单曲循环会让整窗只推 1 首；
- 「我喜欢的音乐」id 用 `specialType=5` 从 `/weapi/user/playlist` 找，取到即缓存；
- 实测：客厅机「从当前歌」1.97s、`从红心歌单` 0.90s 完成（拉推荐 + 替换队列 + 整窗下发）；
  播了 8 秒后进度仍在 17s 处，**没有被整窗重推重置到 0**。

### 8.4.7 小米云端认证（MiNA，2026-11-20 重做）

云端 transport 依赖 `api2.mina.mi.com` 的 `serviceToken`。认证分两代：

- **`passToken`**：长期凭据（社区实测 1~1.5 个月），**每次 `serviceLogin` 会轮换**；
- **`serviceToken`**：会话凭据（Cookie），短命，丢了随时用 passToken 换。

所以持久化的对象必须是 `passToken`（+ 与它绑定的 `deviceId`），`serviceToken` 只是缓存。
这是 `hass-xiaomi-miot` / `miservice` / `go2rtc` 一致的模型。

**登录方式（`musicd -login xiaomi`）**

| 方式 | 本质 | 优点 |
|---|---|---|
| 扫码 / 链接授权 | `longPolling/loginUrl?sid=micoapi` → 用户确认 → `lp` 长轮询 → `location` → `serviceToken` | **免密码、免短信、不触发风控**；二维码内容就是授权链接 |
| 账号密码 + 短信 | `serviceLogin` → `serviceLoginAuth2` → `notificationUrl` → `/identity/*` 发码/验码 → 重走 `serviceLogin` | 无浏览器时可用 |

> 扫码接口实测接受 `sid=micoapi`（返回 `qr` / `loginUrl` / `lp` / `timeout=300`）。
> 授权链接可直接粘到任意浏览器：已登录小米账号就是确认授权，没登录就先登录同一账号。
>
> **STS 401 是正常现象**：`finishLocation` 必须**逐跳**走 redirect 链并在每一跳收集
> `Set-Cookie`。micoapi 的 `https://api2.mina.mi.com/sts` 经常对"已完成的登录"返回
> 401，而 `serviceToken` 在前面某一跳就下发了。用 HTTP 客户端的自动跟随只拿最后一跳，
> 就会误判成"没拿到 token"（`hass-xiaomi-miot` 专门为此定义了
> `MiCloudStsUnauthorized`）。

**旧实现的两个 bug（2026-11-20 修）**

1. `loadTokenLocked` 里 config 的 `pass_token` 优先于认证文件 → 配置里一个过期值
   把文件里有效的 passToken 挡住了（2026-11-20 卧室"鉴权失败"的真因）；
2. `invalidateToken()` 只清内存，`ensureToken()` 又从磁盘把**同一个死 token** 读回来，
   于是"已重登一次"其实一次都没登。

**另一个隐蔽坑**：`serviceLogin` 响应里的 `nonce` 是 **18 位整数**。Go 默认
`json.Unmarshal` 会解成 float64 丢精度，拿它算 `clientSign` 必然失败——表现为
"响应缺少 nonce/ssecurity/location"。必须 `Decoder.UseNumber()`。

**加密存储**：认证文件 AES-256-GCM（magic `XIAOAIAUTH1` + 12B nonce），密钥单独
`0600` 文件。不需要 root：文件属主跟着跑 daemon 的用户走；`-login` 会在**开始前**
检查可写性（否则用户扫完码才在最后一步吃 EACCES）。相对路径的 `token_file` 按
**配置文件目录**解析，不按进程 cwd。收益是"文件被单独复制走等于废纸"；不用账号 id 当盐（公开信息，
方案开源，等于没锁）。sqlite+SQLCipher 对一个 JSON 来说是过度设计。

### 8.4.8 网易云认证（`-login ncm`）

**网易云没有小米那种双轨凭据**，这点决定了它不需要"续期"这套东西：

| | 小米 | 网易云 |
|---|---|---|
| 长期凭据 | `passToken`（约 1~1.5 个月，会轮换） | **`MUSIC_U` 本身就是长期凭据**（数月，直到被踢） |
| 短期凭据 | `serviceToken` | 无 |
| 续期接口 | `serviceLogin` + passToken | **不存在** |
| 二次验证 | 短信 / 图形码 / 风控 | 无（扫码 = 手机上确认） |
| 失效原因 | token 过期 | 改密码 / 退出所有设备 / 风控 / 长期不用 |

所以**不做定时续期**：只要还在用，`MUSIC_U` 就不会闲到失效；硬造一个 refresh 只会增加复杂度。

现存的登录形态（`-login ncm`）已经够用：

```
GetKey() → 二维码 + 链接 → 每 2s CheckQR()
   800 过期重新申请 / 801 待扫 / 802 待确认 / 803 成功
   → jar.Inject(cookie) → Account() 校验 uid/nickname
```

2026-11-21 只补了三件事（**不是重做**）：

1. **加密存储**：`cookie.json` 改用与小米认证同一个容器（AES-256-GCM + 独立密钥
   `cookie.json.key`）。里面是 `MUSIC_U`，泄露等于账号被接管；旧明文文件在
   **加载时立刻**重写成密文，不在盘上多躺。
2. **登录前可写性预检**：跟小米同一个坑——避免"扫完码才报 EACCES"。
3. **启动诊断分档**：区分"没登录（文件不存在）"和"登录已失效（接口返回未登录）"，
   给出的动作都是 `musicd -login ncm`，但原因不再混在一起。

### 8.5 语音链路

```
小爱同学 → mipns(ASR) → ha_agent → Wyoming → HA Assist
  → intent_script（自定义 PlayNetease，或用内置 HassMediaSearchAndPlay）
  → rest_command → musicd /play?q=晴天 周杰伦
  → musicd 返回 {ok, text} + 推送窗口到设备
  → HA 回 speech.text → ha_agent TTS（media=mibrain，自动压住音乐）
```

- 增强点：`musicd` 支持"下一首/上一首/大点声/这是什么歌"这类媒体意图，
  对应内置 `HassMediaNext` / `HassMediaPause` / `HassSetVolume`；
- 现有 `ha_agent` 已有 `player_play_operation` 调用（`action:play/pause`），可直接复用。

### 8.6 已知坑

1. **VIP / 版权灰**：无 cookie 只给 128k 甚至无 url；需登录 + 可能需 UnblockNeteaseMusic。
2. **TLS 与时间**：设备无 RTC，靠 NTP；时间没同步时 https 直链可能证书失败 → **代理走 http 更稳**。
3. **`set_play_resource` 会污染续播记录**：播自建内容仍会向小米云回报 index/position。
   无害，但要知道；要干净就得在 `mediaplayer` 之后拦（暂不做）。
4. **App 点歌会覆盖队列**：若走 `/internal/ubus`，App 选的歌会顶掉 `musicd` 的窗口——先验证再定策略。
5. **`loadmore` 有两条路**：`matool_get_loadmorev2_list`（直连云，仍活）与
   `ubus call mibrain aivs_event_post Nlp LoadMore`（随 aivs 死）。自建队列**不走任何一种**，自己管。
6. **现在走 `payload` 分支**（2026-11-20 迁移）：`audio_type:"MUSIC"` 必须在 payload 顶层，
   `needs_loadmore:0`，每次推窗后 `player_set_loop type:1`。`musics[]` 仍在文档里作对照，
   但它的 `global_id` 只有 int32 宽，是 devID 那套绕行的根源，**不要再走回去**。
   `play_url` 单曲方式仍没有位置回读（media_type≠13 时整块 `play_song_detail` 都不给）。
7. **网易云直链只有 1200 秒**，签名在路径里不可延长 → 不能把直链直接烧进长窗口，
   必须靠 `/s/<songID>` 懒解析（§7.1.2）。
8. **不要试图在设备上跑 musicfox**：Go 二进制 + TUI + 需要 cookie 文件，RAM/交互都不合适。
   musicfox 值得借鉴的是它的 API 层（`github.com/go-musicfox/netease-music`）。

---

## 9. 取证与复现

### 9.1 方法

- **字符串**：`work_tmp/dump_strings.py` / `dump_strings2.py` → `work_tmp/sdump/*.txt`；
- **ubus 签名**：设备上 `ubus -v list mediaplayer`，存档 `work_tmp/inv_ubus.txt`；
- **ubus 方法表/参数策略**：在 `mediaplayer` 里找 `player_play_music` 的字符串指针，
  紧跟其后就是 `{policy[], n_policy, name, handler}`，policy 是明文
  `{字段名, blobmsg 类型}` 数组（`blobmsg_type`：3=string，5=int32，7=bool）；
- **静态反汇编**：`python work_tmp/armxref.py <elf> "<字符串>"` —— 这镜像没有符号表也没有
  section，工具按 program header 逐字解码，做「字符串 VA → 字面量池 → `ldr rX,[pc,#imm]`
  引用点」的两级跳，直接给出每个 JSON key 在哪个函数、第几行被取出来。
  §7.1 的 schema 就是这么跑出来的（`parse_audio_player` = 0x29f50）；
- **脚本**：`rootfs_patched/usr/bin/mphelper` 直接可读；
- **间接证据**：`libmico-common.so` 的未定义符号（谁调谁）。

### 9.2 已确认的关键事实

`upload_play_resource`/`matool_set_play_resource` 的上报字段、`get_loadmore_list` 的云端队列语义、
`mdplay_notify_*` 全部空打、`mi_play_resource`/`mi_play_url`/`mi_play_operation` 是内部分发器、
`parse_audio_player` 的三套 schema（字段名/类型/嵌套，见 §7.1）、
`player_play_url` 的 `type` 钳位与无白名单（§7.2）—— 均可在
`work_tmp/sdump/mediaplayer.txt` 与 `work_tmp/armxref.py` 上复现。

### 9.3 待验证（都需要人在设备旁）

| # | 验证 | 方法 | 能钉死什么 |
|---|---|---|---|
| 1 | App 点歌走哪条通道 | SSH `ubus monitor` 5 分钟 + App 点歌 | `/internal/ubus` vs MIoT vs 已死的 AIVS |
| 2 | 按键后 App 多久变 | 按暂停 + `logread -f \| grep -E 'mediaplayer\|miio'` | 推送还是拉取 |
| 3 | 进度条是否插值 | 记 App 位置，30s 后对比 `player_get_play_status` | App 是否本地补秒 |
| 4 | 整窗下发是否真的自动接歌 | 塞 2 条自建直链，听是否自动播第 2 首、按 next 是否切 | ✅ **已实测**：自动接歌成立、`player_play_operation next` 有效（§7.1） |
| 5 | 设备是否跟随 302 | 下发 `http://nas:8088/s/<id>`（302）与 proxy 模式各试一次 | ✅ **已实测**：跟随 302，`206 audio/mpeg`；`musicd` 默认 redirect |
| 6 | 机身按键能否切歌 | 按上/下一首 | ✅ **已排除**：本型号只有 4 个动作，无 next/prev（§4.1） |
