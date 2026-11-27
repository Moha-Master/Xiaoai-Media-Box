# 小爱盒子 · Xiaoai Media Box

在小爱音箱上调用原生方式播放自定义内容。

> 项目理论上不限定设备型号；目前只在 `xiaomi.wifispeaker.lx05` 上做过完整实测，
> 文档中涉及设备行为的部分均以该型号为准。

```
        手机 / HA / curl
             │ HTTP
             ▼
   ┌──────────────────┐   网易云 API(cookie)     ┌───────────────┐
   │      xiaoai-mb   │ ───────────────────────► │ music.163.com │
   │  队列真源 / 直链 │ ◄─────────────────────── │               │
   └────────┬─────────┘       直链 + expi        └───────────────┘
            │ ① player_play_url(http://nas:8088/s/<id>)
            │ ② 设备来 GET /s/<id> → 302 到新鲜直链
            ▼
   ┌──────────────────────┐
   │ 小爱音箱 mediaplayer │  原生解码 / 音量 / ducking / 按键 / App 状态
   └──────────────────────┘
```

## 快速开始

```bash
# 构建到 bin/（会先构建前端：首次自动 npm ci，之后 vite build 到 internal/app/web/dist）
make build

# 1) 网易云扫码登录
./bin/xiaoai-mb -login ncm

# 1b) 小米账号登录
./bin/xiaoai-mb -login xiaomi

# 1c) 检查和更新小米凭据
./bin/xiaoai-mb -auth-check

# 2) 使用默认工作目录启动服务
./bin/xiaoai-mb

# 自定义工作目录：
./bin/xiaoai-mb -dir ~/somewhere/xiaoai-mb
```

## 项目结构

```
cmd/xiaoai-mb/        程序入口：flag 解析、登录、装配与启停
internal/config/       配置类型、默认值、归一化、校验与原子保存（含备份/回滚）
internal/auth/         加密认证文件容器、cookie jar
internal/netease/      网易云 API 封装与直链解析
internal/queue/        播放队列模型（循环/随机/整窗/快照/编辑）
internal/player/       播放器接口、noop 与 ubus-over-ssh 实现
internal/xiaomi/       小米 MiNA 云端通道（登录、ubus、闹钟音量）
internal/app/          App/Session、HTTP 服务、轮询与 SSE、管理面、WebUI 静态服务
internal/app/web/dist/ 前端构建产物（go:embed，不进 git，只留 .gitkeep 占位）
internal/util/         跨包小工具（宽松 JSON 取值、明文 JSON 原子读写等）
web/                   前端源码：Vue 3 + Vuetify 4 + TS（src/{api,stores,components,views}）
custom_components/     Home Assistant 集成（HACS 布局）
scripts/               音箱端守护脚本：state_reporter.sh（状态主动上报）+ .initd / .conf.example
docs/                  设备侧协议调研记录
```

## 内置 WebUI

浏览器打开 `http://<host>:<port>/` 访问 WebUI 以使用项目的主要功能。

## 配置

复制 `config.example.json` 为工作目录下的 `config.json` 再改，或用命令行覆盖。字段：

| 字段 | 说明 |
|---|---|
| `listen` | 监听地址，默认 `0.0.0.0:8088`。**不能填 `127.0.0.1`**，否则音箱连不进来 |
| `public_base` | **设备能访问到的本服务地址**，填这台机器的局域网 IP，如 `http://192.168.1.10:8088`。填 `127.0.0.1` 会让音箱拿到打不开的 URL，表现为“播放失败，请检查网络” |
| `cookie_file` | 网易云 cookie（**加密**，含 `MUSIC_U`） |
| `cookie_key_file` | cookie 加密密钥，默认 `cookie_file + ".key"` |
| `quality` | `standard` / `higher` / `exhigh`（默认）/ `lossless` / `hires` / `jyeffect` / `sky` / `jymaster` |
| `window` | 整窗下发的窗口大小（默认 20，与设备 `track_list` 容量一致；上限 200） |
| `loop` | 启动时的播放模式：`none`（播完就停）/ `list` / `single` / `shuffle`（= list + 随机） |
| `stream_mode` | `redirect`（默认，302 直连 CDN）或 `proxy`（本机中转，省设备直连） |
| `queue_persist` | 播放队列快照开关，默认 `true`；设 `false` 则重启后队列清空、无限心动退回一次性（见下） |
| `speakers[]` | **音箱列表**（见下） |
| `xiaomi` | 云端 MiNA 的账号凭据（见下）；只配 ssh 音箱时可以不填 |

**`speakers[]` 每项：**

| 字段 | 说明 |
|---|---|
| `id` | 稳定标识（HA 的唯一性用它），不填按序号生成 |
| `name` | 显示名，如 `客厅` |
| `control` | **不填默认 `cloud`**；可选 `ssh` / `noop` |
| `status_mode` | 状态感知方式：`cloud`（默认）\| `push`（**推荐给刷机设备**，监听音箱主动推送）\| `ssh`（SSH 轮询）\| `none` |
| `device_id` | `cloud`：音箱的 MiNA deviceID（UUID）；不填则账号下只有一台在线时自动选。**`ssh` 音箱也建议填**——闹钟音量等“云端独有能力”要用它 |
| `ssh_host` | `ssh`：如 `root@192.168.1.20`；**control=ssh 时必填**，否则启动直接报错 |
| `ssh_key` | `ssh`：私钥路径；**留空用 ssh 默认密钥**（推荐），不要填别的用户的路径 |
| `media` | 通道：`ssh` 用 `common`，`cloud` 用 `app_ios`（默认按 control 填） |
| `play_type` | `player_play_url` 的 `type`，默认 1（TTS 才是 4） |
| `use_window` | 走 `player_play_music` 整窗下发，默认 `true` |
| `status_poll` | **稳态**回读间隔（秒）；`ssh` 默认 15，`cloud` 默认 60；写 `-1` 完全关闭。原厂是"云 5s 拉设备 + 60s 属性快照"，我们没必要更勤 |
| `poll_burst` | **不稳定态**回读间隔（秒），默认 5。任何"当前状态不可靠"的时刻（我们发了指令、设备换了歌、播放态翻转、位置对不上、探测从失败恢复）会切到这一档 |
| `poll_rounds` | 不稳定态持续几轮，默认 3（即 5s×3 后自动回落稳态）；重复触发只重置余量、不叠加 |
| `poll_drift_ms` | 服务端估算与设备真实位置差多少毫秒算"不可靠"；默认 `ssh` 4000、`cloud` 8000（MiNA 起播延迟大，阈值太紧会让 burst 停不下来） |
| `report_interval` / `report_hb_play` / `report_hb_idle` / `report_seek_tol_ms` | **仅 `status_mode: "push"`**：服务端判定用的 daemon 节奏期望值，必须与音箱上 `/data/etc/state_reporter.conf` 一致（默认 5 / 15 / 30 / 8000）。静默阈值由心跳推出，改了这里不改 daemon（或反之）就会误判离线 |

**`xiaomi`（云端才需要）：**

| 字段 | 说明 |
|---|---|
| `user` | 小米账号 userId（可留空，登录后从认证文件读） |
| `device_id` | **登录设备 id**（与 passToken 绑定，必须稳定），不是音箱的 UUID |
| `password` | **只给 `-login xiaomi` 用**；留空则登录时提示输入（不回显）。运行时永不使用 |
| `token_file` | 认证文件（AES-256-GCM 加密，`0600`） |
| `key_file` | 加密密钥（`0600`），默认 `token_file + ".key"` |

### 状态感知模式（`status_mode`）

每个音箱的主动控制（`control`）与状态感知（`status_mode`）已完全正交解耦：

| 模式 | 适用场景 | 说明 |
|---|---|---|
| `cloud`（**默认**） | 原厂设备 / 未刷机音箱 | 服务端通过小米云端 MiNA 每 60s 稳态轮询状态 |
| `push`（**推荐**） | 已获取 root/SSH 权限的音箱 | 音箱端运行 `scripts/state_reporter.sh` 守护脚本主动向服务端推送状态。机身按键感知延迟从最坏 60s 降到 **1~2s**，且服务端不再发起任何 SSH 轮询 |
| `ssh` | 调试 / 不愿常驻 daemon 的刷机音箱 | 服务端通过 SSH 定时执行 ubus 轮询 |
| `none` | 关闭状态回读 | 纯本地计时推算 |

#### 启用音箱端主动上报（`push` 模式）

如果获取了音箱的 ssh 权限，上一个极小的 POSIX shell 守护脚本完成：

1. **服务端**：该音箱设 `status_mode: "push"`；
2. **音箱**：将仓库内 `scripts/state_reporter.sh` 放置到音箱的文件系统中 `/<somewhere_you_want>/state_reporter.sh`；
   仓库内 `scripts/state_reporter.conf.example` 放置到音箱的文件系统中 `/data/etc/state_reporter.conf` 并填写实际的 `url`；
3. **开机自启**：将仓库内 `scripts/state_reporter.initd` 放置到音箱的文件系统中 `/etc/init.d/state_reporter`，
   再 `ln -s ../init.d/state_reporter /etc/rc.d/S78state_reporter`。

手工试跑（不装 init.d，命令行首参覆盖 conf 里的 url）：

```bash
sh /tmp/state_reporter.sh "http://<本服务局域网IP>:8088/api/device/state?speaker=<音箱ID>"
```
`；全程**零磁盘写入**。
