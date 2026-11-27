# Xiaoai Media Box (Home Assistant 集成)

配套 `xiaoai-mb` 服务（本仓库根目录）的 HA 控制器。
**集成只做控制面**：网易云、队列、直链、设备通信都在 `xiaoai-mb` 里，HA 这边
只有 config flow + coordinator + `media_player` 实体。

## 安装

仓库采用 HACS 标准布局（根目录 `hacs.json` + `custom_components/xiaoai_mb/`）。

**方式一：HACS（推荐）**

1. HACS → 集成 → 右上角菜单 → 自定义存储库，填入本仓库地址，类型选「集成」；
2. 搜索 `Xiaoai Media Box` 并安装；
3. 重启 Home Assistant。

**方式二：手动**

1. 把 `custom_components/xiaoai_mb/` 整个目录复制到 HA 的 `custom_components/` 下：

   ```bash
   # 例：Docker 容器部署时复制进挂载的 config 目录
   cp -r custom_components/xiaoai_mb <HA_CONFIG_DIR>/custom_components/
   ```

2. 重启 Home Assistant。
3. 「设置 → 设备与服务 → 添加集成 → Xiaoai Media Box」，填 `xiaoai-mb` 地址
   （默认 `http://192.168.1.10:8088`），下一步会列出所有音箱，**多选**，
   每台生成一个独立设备。

## 能做什么

- **一个配置条目管理一台 xiaoai-mb、多台音箱**：每台一个独立 `media_player` 设备，
  队列/状态/音量互不干扰；
- **Media Browser**：浏览「全局搜索」「当前队列」「每日推荐」「心动·当前」「心动·红心」「我的歌单」；
  - 「全局搜索」是唯一能触发搜索的入口（HA 前端**只在非根页面**渲染搜索框，
    根页面无论如何都不会有搜索框）；
  - 「当前队列」点任意一首是**在当前队列里跳播**（`jump:<index>`），不是新建队列；
  - 「心动·当前 / 心动·红心」和歌单一样是**点一下就播**的入口：前者以正在放的歌为种子，
    后者从红心歌单随机挑一首；播起来之后会自动无限续杯（见服务端 README）；
  - 点歌单直接整单播放，点曲目单曲播放；
- **播放模式**：`REPEAT_SET`（关闭 / 列表循环 / 单曲循环）+ `SHUFFLE_SET`（随机），
  两者正交，落到 `xiaoai-mb` 的 `/api/mode`；
- **进度跳转**：`SEEK` → `/api/seek`（设备 `player_set_positon`，服务端会回读确认，
  没到位自动重试）；卡片上的进度条可以直接拖；
- **静音**：`VOLUME_MUTE` → `/api/mute`。设备没有独立 mute 接口，所以是
  「音量置 0 + 记住原值」；`volume_level` 在静音期间保持原值，不会被拽到 0；
- **加入队列**：`MEDIA_ENQUEUE` → `/api/enqueue`，`enqueue: next` 插到当前曲后面、
  `enqueue: add` 加到队尾，**都不打断正在播的这一首**（内部靠“重推整窗 + seek 回原位”）；
- **红心**：每台音箱一个 `switch.<speaker>_红心`，state 就是当前曲是否已收藏，
  `turn_on` / `turn_off` 直接调网易云收藏接口（HA 的 media_player 没有 LIKE 这个
  feature，所以只能做成独立实体）；`media_player` 上也有一份 `liked` 属性；
- **心动模式**：媒体浏览器根节点里的「心动·当前 / 心动·红心」，**和歌单一样点一下就播**：
  拿种子歌向网易云要一批推荐、**替换**当前队列，之后由服务端无限续杯。
  「心动·当前」会把种子排第一并把进度拉回原处（先把手头这首放完再过渡），
  「心动·红心」从「我喜欢的音乐」里随机挑一首当种子。执行完队列 `source`
  会显示 `心动模式·<种子>`；
- **闹钟音量**：每台音箱一个 `number.<speaker>_闹钟音量`（0-100），走云端
  `alarm.alarm_set_volume`，与播放音量/静音互不影响；设备需要在配置里带 `device_id`；
- **歌词**：每台音箱一个 `sensor.<speaker>_歌词`，state 就是**正在唱的那一行**，
  属性带 `next_line` / `line_index` / `line_count` / `song_id`。时间轴完全由 `xiaoai-mb` 算，
  行号一变就推一帧过来，实体自己不发任何请求；
- **搜索**：进入「全局搜索」后顶部有搜索框；`media_player.play_media` 传纯文本
  （如 `晴天`）也走 `xiaoai-mb` 搜索；Assist 的「播放 晴天」意图同样走这条路
  （`HassMediaSearchAndPlay` → `search_media`）；
- **状态同步靠 SSE，不靠轮询**：集成连上 `xiaoai-mb` 的 `/api/events`，xiaoai-mb 只在"真变化"时推完整快照，
  HA 收到就刷新实体（`event_stream` 属性可看当前是 `connected` 还是 `polling`）。
  流断了自动指数退避重连，期间退回 60 秒兜底轮询，功能不瘸；
- 进度条：HA 用 `media_position` + `media_position_updated_at` 本地插值，
  帧到达时校正（操作后立即 `fresh=1` 强制回读，不等下一帧）；
- 音量：`VOLUME_SET` → `xiaoai-mb` `/api/volume`（和米家控制的是同一个设备全局音量）。

## 心动模式怎么用

在媒体浏览器里点「心动·当前」或「心动·红心」即可；也可以直接调 `play_media`：

```yaml
# 卡片按钮 / 自动化：从红心歌单开始心动
- service: media_player.play_media
  target:
    entity_id: media_player.hou_ting_xiao_ai_yin_xiang_play
  data:
    media_content_id: hotmode:liked
    media_content_type: playlist
```

「心动·当前」在队列为空时会报「当前没有在放的歌」，这是预期行为——
没在放的时候用「心动·红心」。

## 卡片上怎么放红心 / 歌词

HA 原生 `媒体控制` 卡片没有按钮位，两种做法：

```yaml
# ① mini-media-player（已装）：按钮区加一个红心，点它就切换收藏
type: custom:mini-media-player
entity: media_player.hou_ting_xiao_ai_yin_xiang_play
buttons:
  - type: service
    id: switch.toggle
    data: {entity_id: switch.hou_ting_xiao_ai_yin_xiang_play_hong_xin}
    icon: mdi:heart
# ② 任意卡片的实体行：把歌词当第二行显示
type: entities
entities:
  - entity: media_player.hou_ting_xiao_ai_yin_xiang_play
  - entity: switch.hou_ting_xiao_ai_yin_xiang_play_hong_xin
  - entity: sensor.hou_ting_xiao_ai_yin_xiang_play_ge_ci
```

语音侧（Assist 说「收藏这首歌」）用脚本即可，红心不需要实体也能操作：

```yaml
alias: 收藏当前这首歌
sequence:
  - service: switch.turn_on
    target: {entity_id: switch.hou_ting_xiao_ai_yin_xiang_play_hong_xin}
```

## media_id 约定

| 形式 | 动作 |
|---|---|
| `song:186016` / `track:186016` | 单曲播放 |
| `playlist:3778678` | 整单播放 |
| `album:12345` | 整张专辑 |
| `daily` | 每日推荐 |
| `hotmode:current` | 心动模式·以当前歌为种子 |
| `hotmode:liked` | 心动模式·从红心歌单随机挑一首 |
| `search:周杰伦` | 搜索并播放（取搜索结果整批入队） |
| 纯文本 `晴天` | 当搜索词 |

## 播放模式映射

| HA | `xiaoai-mb` |
|---|---|
| `repeat: off`（关闭） | `loop=none`：队列播完就停 |
| `repeat: all`（列表循环） | `loop=list` |
| `repeat: one`（单曲循环） | `loop=single` |
| `shuffle: on/off` | `shuffle=true/false`（独立开关，与 repeat 正交） |

## 当前限制

- **HA→xiaoai-mb 这一跳已经没有延迟了**（SSE，实测点"下一首"1.33 秒可见）；
  剩下的滞后全在 **xiaoai-mb→设备** 那一跳：设备不会主动上报（实测见
  `docs/MEDIA-ARCH.md` §8.4.3），只能靠回读，稳态间隔 `status_poll`（当前两台都 60s）。
  所以**机身按键 / 米家操作**的外部变更最坏晚 60 秒；
- ⚠ **混叠（aliasing）**：外部暂停如果持续时间短于一个采样周期，会被**完全漏掉**
  （实测 36 秒的暂停落在两次 60s 探测之间，HA 状态从没翻转）。
  要根治只有一条路：接上设备本地的埋点事件流（`tail -F /tmp/stat_points_rom.log`，
  实测按键 1 秒内可见），见 `docs/FEATURE-MAP.md` §16.2；
- 想要更跟手又不想等事件流，可把 `status_poll` 调小（ssh 机代价很低，一次 ubus ~50ms；
  云端机要照顾 MiNA）；
- 升级到多音箱后，**旧的单音箱配置条目仍能加载**（只有一台设备）；
  想要多选，删除旧条目后重新添加一次即可；
- 「当前队列」是浏览节点，不是 HA 原生播放列表控件（HA 的媒体播放器卡片没有队列视图）；
- 搜索是**全网**搜索：进入「全局搜索」后搜索框的效果与所在歌单无关
  （歌单内的小输入框是前端做的本地筛选，不是搜索）；
- 没有自定义服务；全部走标准 `media_player` 服务，方便自动化和 Assist。
