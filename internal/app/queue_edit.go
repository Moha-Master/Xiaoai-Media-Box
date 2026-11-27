package app

import (
	"context"
	"errors"
	"log"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/player"
)

// errQueueEmpty 给 resumePending 用（与其它地方的文案一致）。
var errQueueEmpty = errors.New("队列为空")

// 队列编辑（删除 / 重排）之后的“设备侧跟随”。
//
// 设备只认我们推过去的那一窗，服务端队列改了它并不知道。所以：
//   - 正在播：重推整窗，再把进度 seek 回去（当前曲没变时做到“听不出断点”）；
//   - 暂停中：什么声音都不出，只记一个标记，等 resume 时重推 + seek；
//   - 删掉了最后一首正在播的歌：停设备，队列停在上一首。
//
// 进度恢复依赖 seekVerified：设备刚收完新窗口会静默忽略 set_positon，
// 直接调用 Seek 已经自带回读重试，这里不用再兜一层。

// setPendingEdit 记录“恢复播放时需要重推窗口”，并保留当前进度。
func (s *Session) setPendingEdit(pos int) {
	s.qEditMu.Lock()
	defer s.qEditMu.Unlock()
	s.pendEdit = true
	s.pendPos = pos
}

// pendingEdit 读取待同步标记（不清除，由真正推完窗口后 clear）。
func (s *Session) pendingEdit() (bool, int) {
	s.qEditMu.Lock()
	defer s.qEditMu.Unlock()
	return s.pendEdit, s.pendPos
}

func (s *Session) clearPendingEdit() {
	s.qEditMu.Lock()
	defer s.qEditMu.Unlock()
	s.pendEdit = false
	s.pendPos = 0
}

// applyQueueEdit 在队列已经被改动之后调用。
//
// curBefore 是编辑前的当前曲 id（用来判断当前曲本身有没有被删/被换掉），
// posBefore 是编辑前估算的播放位置，wasPlaying 是编辑前的播放态。
func (s *Session) applyQueueEdit(ctx context.Context, curBefore int64, posBefore int, wasPlaying bool) error {
	if s.q.Len() == 0 {
		s.clearPendingEdit()
		if wasPlaying && s.player != nil {
			if err := s.player.Stop(ctx); err != nil {
				return err
			}
		}
		s.q.MarkPaused()
		s.probeSoon("队列已清空")
		return nil
	}

	nowPlaying := s.q.View(0).Playing
	cur, _, _ := s.q.Current()
	currentChanged := cur.ID != curBefore

	if !wasPlaying {
		// 暂停中：不出声，等 resume 时统一处理
		s.setPendingEdit(posBefore)
		return nil
	}
	if !nowPlaying {
		// 删掉的正是最后一首正在播的歌：后面没内容了，停设备
		s.clearPendingEdit()
		if s.player != nil {
			if err := s.player.Stop(ctx); err != nil {
				return err
			}
		}
		s.q.MarkPaused()
		s.probeSoon("删除当前曲")
		return nil
	}

	if s.windowMode {
		wp, ok := s.player.(player.WindowPlayer)
		if !ok {
			return nil
		}
		// pushWindow 从当前曲 0 秒开始播，进度先归零，再按需 seek 回去
		s.q.SyncPosition(0, true)
		if err := s.pushWindow(ctx, wp); err != nil {
			return err
		}
		if !currentChanged && posBefore > 0 {
			return s.Seek(ctx, posBefore)
		}
		return nil
	}

	// 单曲模式：设备手里是一条 /s/<id> 直链，只有当前曲被换掉时才需要重新下发
	if currentChanged {
		return s.startCurrent(ctx)
	}
	return nil
}

// resumePending 处理“暂停时改过队列”的恢复：先重推窗口再跳回原进度。
// 返回 handled=true 表示已经自己完成了恢复，调用方不必再走普通 Resume。
func (s *Session) resumePending(ctx context.Context) (bool, error) {
	pend, pos := s.pendingEdit()
	if !pend {
		return false, nil
	}
	if s.q.Len() == 0 {
		s.clearPendingEdit()
		return true, errQueueEmpty
	}

	if s.windowMode {
		wp, ok := s.player.(player.WindowPlayer)
		if !ok {
			s.clearPendingEdit()
			return false, nil
		}
		s.q.SyncPosition(0, true)
		if err := s.pushWindow(ctx, wp); err != nil {
			return true, err
		}
	} else if err := s.startCurrent(ctx); err != nil {
		return true, err
	}
	if pos > 0 {
		if err := s.Seek(ctx, pos); err != nil {
			// 位置没跳回去不算致命：窗口/曲目已经对了，探针会按设备回报纠偏
			log.Printf("[%s] 队列编辑后恢复进度失败：%v", s.speaker.Name, err)
		}
	}
	s.clearPendingEdit()
	return true, nil
}
