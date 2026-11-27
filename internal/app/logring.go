package app

import (
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

// logRing 是最近若干行日志的环形缓冲。
//
// 服务的日志走标准库到 stderr（systemd 收进 journal），网页管理面想「就地看一眼日志」，
// 所以把同一份输出多抄一份在内存里 —— 不去调 journalctl：那要多一份权限，
// 容器里也未必有。
type logRing struct {
	mu    sync.Mutex
	max   int
	lines []string
	total int // 累计写过的行数（含被挤掉的），前端可以拿它判断有没有新内容
}

func newLogRing(max int) *logRing {
	if max <= 0 {
		max = 500
	}
	return &logRing{max: max, lines: make([]string, 0, max)}
}

// Write 实现 io.Writer。标准库每次日志是一条完整的行，但多行文本也可能一次写进来，
// 所以按换行切，丢弃空行。
func (r *logRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, line := range strings.Split(string(p), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(r.lines) == r.max {
			copy(r.lines, r.lines[1:])
			r.lines[len(r.lines)-1] = line
		} else {
			r.lines = append(r.lines, line)
		}
		r.total++
	}
	return len(p), nil
}

// Tail 返回最近 n 行（时间正序）和累计行数。
func (r *logRing) Tail(n int) ([]string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n > len(r.lines) {
		n = len(r.lines)
	}
	out := make([]string, n)
	copy(out, r.lines[len(r.lines)-n:])
	return out, r.total
}

// InstallLogRing 把标准库日志同时写进内存环形缓冲（保留最近的 500 行）。
//
// 在 NewApp 里调用而不是 main：App 是唯一需要这份日志的人，
// 而且测试里构造 App 时也自动有日志可读。
func (a *App) InstallLogRing(max int) {
	a.logs = newLogRing(max)
	log.SetOutput(io.MultiWriter(os.Stderr, a.logs))
}
