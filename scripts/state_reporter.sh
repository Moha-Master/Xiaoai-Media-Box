#!/bin/sh
# state_reporter.sh — 小爱音箱设备侧状态主动上报守护脚本
#
# 干什么：在音箱本地每 `interval` 秒读一次 mediaplayer 状态，只在「有意义」时
# 把 ubus 的原始回复 POST 给 xiaoai-mb，让服务端免掉 SSH/云端轮询也能秒级知道
# 播放态、切歌、音量和外部 seek。
#
# 上报时机（三条，任一成立才发一个包）：
#   1. 事件：status / volume / audio_id 任一与上次成功上报时不同 → 立即发；
#   2. 位置异常：position 相对上次上报的增量与「应该怎样」不符 ——
#      播放中应 ≈ 真实经过时间，暂停/停止时应为 0；偏差超过 seek_tol 或倒退
#      （拖回 / 循环绕回 / 外部 seek）→ 立即发。暂停态也要判，否则暂停中拖
#      进度条要等 30 秒心跳才看得见，服务端的 seek 复核也就拿不到确认帧。
#   3. 心跳：都没变时，播放中每 hb_play 秒、暂停/空闲每 hb_idle 秒发一次，
#      用来纠服务端本地插值的漂移，也是服务端的存活依据（>60s 静默判离线）。
#
# 设计约束（改动前请读，都是实测踩出来的）：
#   · 零写盘：不产生任何日志文件；错误只走 logger（本固件 syslog 在 tmpfs）。
#   · 不用 eval：awk 输出先做字符白名单净化，再整体赋给**一个**变量，最后用
#     ${var%%|*} 逐个取值。audio_id 会被 DLNA 投屏 metadata、米家下发的 payload
#     等内容写入，`eval $(...)` 等于把云端字符串送进 root shell（已实机验证可执行）。
#   · awk 里**不要**在规则块中初始化变量：ubus 的回复是多行的，逐行重置会把
#     已经提取到的值清空（实测得到空结果）。只在 match 成功时赋值即可。
#   · ubus 必须带 -t：mediaplayer 卡死时不带超时的 `ubus call` 会永久挂起
#     （实测 SIGSTOP mediaplayer 后，无 -t 6 秒仍未返回，-t 3 则 3 秒自己退出）。
#   · 时钟用 /proc/uptime：单调、免 fork、不受 RTC 回跳影响（设备开机早期会从
#     2023-12-01 跳到真实时间，用 date +%s 算 elapsed 会变负，心跳就再也不发了）。
#     注意要用默认 IFS 的 `read -r up _`，写成 `IFS= read` 会把整行塞进第一个变量。
#   · curl 失败不更新基线：否则这次变化被当成「已上报」，要等一个心跳才补。
#     基线不推进时 elapsed 会自然变大，expected 跟着变大，不会误判成 leap。
#
# 配置：/data/etc/state_reporter.conf（本固件既有约定，被 `.` 直接 source）
#   url=http://192.168.1.10:8088/api/device/state?speaker=living
#   interval=5        # 本地采样间隔（秒）
#   ubus_timeout=5    # ubus call 超时（秒）
#   hb_play=15        # 播放中心跳（秒）
#   hb_idle=30        # 暂停/空闲心跳（秒）
#   seek_tol=8000     # 位置异常容差（毫秒）
# 第一个命令行参数覆盖 url（方便手工试跑）。

CONF=/data/etc/state_reporter.conf

url=""
interval=5
ubus_timeout=5
hb_play=15
hb_idle=30
seek_tol=8000

[ -f "$CONF" ] && . "$CONF"
[ -n "$1" ] && url="$1"

if [ -z "$url" ]; then
    logger -t state_reporter -p 3 "缺少 url（\$CONF 或第一个参数），退出"
    exit 1
fi

last_key=""
last_pos=""
last_sent=0
first=1

while true; do
    raw=$(ubus -t "$ubus_timeout" call mediaplayer player_get_play_status 2>/dev/null)

    # 单调时钟（整数秒），免 fork
    read -r up _ < /proc/uptime
    now=${up%%.*}

    # 一次 awk 取四个字段并做白名单净化（详见文件头的设计约束）
    sig=$(printf '%s' "$raw" | awk '
    {
        if (match($0, /\\"status\\"[[:space:]]*:[[:space:]]*[0-9]+/)) {
            t = substr($0, RSTART, RLENGTH); sub(/.*:[[:space:]]*/, "", t); s = t
        }
        if (match($0, /\\"volume\\"[[:space:]]*:[[:space:]]*[0-9]+/)) {
            t = substr($0, RSTART, RLENGTH); sub(/.*:[[:space:]]*/, "", t); v = t
        }
        if (match($0, /\\"audio_id\\"[[:space:]]*:[[:space:]]*\\"[^\\"]*\\"/)) {
            t = substr($0, RSTART, RLENGTH)
            sub(/.*:[[:space:]]*\\"/, "", t); sub(/\\".*$/, "", t); a = t
        }
        if (match($0, /\\"position\\"[[:space:]]*:[[:space:]]*[0-9]+/)) {
            t = substr($0, RSTART, RLENGTH); sub(/.*:[[:space:]]*/, "", t); p = t
        }
    }
    END {
        gsub(/[^0-9]/, "", s); gsub(/[^0-9]/, "", v); gsub(/[^0-9]/, "", p)
        gsub(/[^A-Za-z0-9_.:-]/, "", a)
        printf "%s|%s|%s|%s", s, v, a, p
    }')

    # 不用 eval：整串赋值不分词、不二次展开，再按 | 逐个切出来
    st=${sig%%|*}
    r1=${sig#*|}
    vol=${r1%%|*}
    r2=${r1#*|}
    aid=${r2%%|*}
    pos=${r2#*|}

    # 解析不出 status 就别 POST（ubus 报错时 stdout 通常是空的，这里是第二层防护）
    if [ -z "$st" ]; then
        sleep "$interval"
        continue
    fi

    key="$st|$vol|$aid"
    send=""

    if [ "$first" = 1 ] || [ "$key" != "$last_key" ]; then
        send="event"
    else
        elapsed=$(( now - last_sent ))
        # 位置异常检测：播放中 position 应该跟着真实时间走，暂停/停止时应该不动。
        # 两种情况的偏差都只可能是有人在 seek（机身、米家、DLNA，或我们自己的
        # 跳转被设备静默忽略后需要复核），所以暂停态也要判——否则暂停中拖进度条
        # 要等一个 30 秒心跳才看得见。
        if [ -n "$pos" ] && [ -n "$last_pos" ]; then
            if [ "$st" = 1 ]; then
                expected=$(( elapsed * 1000 ))
            else
                expected=0
            fi
            delta=$(( pos - last_pos ))
            if [ "$delta" -lt 0 ]; then
                send="rewind"
            elif [ $(( delta - expected )) -gt "$seek_tol" ]; then
                send="leap"
            fi
        fi
        if [ -z "$send" ]; then
            if [ "$st" = 1 ]; then
                [ "$elapsed" -ge "$hb_play" ] && send="hb"
            else
                [ "$elapsed" -ge "$hb_idle" ] && send="hb"
            fi
        fi
    fi

    # 只有 POST 成功才推进基线；失败下一轮自然重试
    if [ -n "$send" ]; then
        if curl -s -m 2 -o /dev/null -X POST -H 'Content-Type: application/json' \
            -d "$raw" "$url" 2>/dev/null; then
            last_key="$key"
            last_pos="$pos"
            last_sent="$now"
            first=0
        fi
    fi

    sleep "$interval"
done
