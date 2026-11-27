package app

import (
	"sync"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/player"
)

// 设备状态跟踪：**纯 pull**。每个 Session 各有一份 tracker。
type deviceTracker struct {
	mu  sync.Mutex
	st  player.DeviceStatus
	at  time.Time
	ok  bool
	err string
	gen uint64 // 每拿到一次新数据 +1：调度器只同步“新数据”，不会反复拿过期位置去锚定
}

func (t *deviceTracker) put(st player.DeviceStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.st, t.at, t.ok, t.err, t.gen = st, time.Now(), true, "", t.gen+1
}

func (t *deviceTracker) fail(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ok = false
	if err != nil {
		t.err = err.Error()
	}
}

// generation 返回数据代次（配合 deviceFresh 使用）。
func (t *deviceTracker) generation() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.gen
}

func (t *deviceTracker) get() (player.DeviceStatus, time.Time, bool, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st, t.at, t.ok, t.err
}
