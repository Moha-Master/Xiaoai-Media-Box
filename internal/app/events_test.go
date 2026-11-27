package app

import (
	"context"
	"testing"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/queue"
)

func newEventSession() (*App, *Session) {
	a := &App{}
	tracks := []netease.Track{{ID: 1, Name: "a", Duration: 100000}, {ID: 2, Name: "b", Duration: 100000}}
	q := queue.NewQueue(queue.LoopList)
	q.Replace(tracks, "test", 0)
	s := &Session{
		app:      a,
		speaker:  config.SpeakerConfig{ID: "living", Name: "客厅", StatusPoll: 60},
		q:        q,
		wakePoll: make(chan struct{}, 1),
		volume:   30,
	}
	a.sessions = map[string]*Session{"living": s}
	a.order = []string{"living"}
	return a, s
}

func waitFor(t *testing.T, ch <-chan sseEvent, want string) sseEvent {
	t.Helper()
	for {
		select {
		case ev := <-ch:
			if ev.Topic == want {
				return ev
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("没等到 %s 帧", want)
		}
	}
}

func TestFirstFrameIsNotAnEvent(t *testing.T) {
	a, s := newEventSession()
	ch, cancel := a.hub.subscribe()
	defer cancel()
	s.emitStateIfChanged() // 第一次只记签名，不该推
	select {
	case ev := <-ch:
		t.Fatalf("启动后的第一帧不该推送：%+v", ev)
	case <-time.After(120 * time.Millisecond):
	}
	s.q.MarkPlaying()
	s.emitStateIfChanged()
	ev := waitFor(t, ch, "state")
	if ev.Speaker != "living" {
		t.Fatalf("speaker=%v", ev.Speaker)
	}
	q, ok := ev.Payload["queue"].(queue.QueueView)
	if !ok || !q.Playing {
		t.Fatalf("payload 里 playing 不对：%+v", ev.Payload)
	}
}

func TestNoDuplicateFrames(t *testing.T) {
	a, s := newEventSession()
	ch, cancel := a.hub.subscribe()
	defer cancel()
	s.emitStateIfChanged()
	s.q.MarkPlaying()
	s.emitStateIfChanged()
	waitFor(t, ch, "state")
	// 同样的状态再 tick 十次也不该再推
	for i := 0; i < 10; i++ {
		s.emitStateIfChanged()
	}
	s.q.SyncPosition(5000, true) // 位置变化不算状态变化
	s.emitStateIfChanged()
	select {
	case ev := <-ch:
		t.Fatalf("位置变化不该触发推送：%+v", ev)
	case <-time.After(120 * time.Millisecond):
	}
}

func TestLyricFrameOnLineChange(t *testing.T) {
	a, s := newEventSession()
	ch, cancel := a.hub.subscribe()
	defer cancel()
	s.emitStateIfChanged()
	s.q.MarkPlaying()
	s.q.SyncPosition(0, true)
	s.emitLyricIfChanged() // 无歌词缓存（ne=nil）→ index=-1，记为第一帧
	waitFor(t, ch, "lyric")
	s.emitLyricIfChanged()
	select {
	case ev := <-ch:
		t.Fatalf("同一行不该重复推送：%+v", ev)
	case <-time.After(120 * time.Millisecond):
	}
}

func TestHubDropsForSlowSubscriber(t *testing.T) {
	a, _ := newEventSession()
	ch, cancel := a.hub.subscribe()
	defer cancel()
	for i := 0; i < sseQueue+20; i++ {
		a.hub.publish(sseEvent{Topic: "state", Speaker: "living"})
	}
	n := 0
	for {
		select {
		case <-ch:
			n++
			continue
		default:
		}
		break
	}
	if n != sseQueue {
		t.Fatalf("慢订阅者应只留 %d 帧，实际 %d", sseQueue, n)
	}
}

func TestStatePayloadShape(t *testing.T) {
	_, s := newEventSession()
	s.q.MarkPlaying()
	s.q.SyncPosition(1234, true)
	p := s.statePayload(context.Background(), false)
	if p["speaker"] != "living" {
		t.Fatalf("speaker=%v", p["speaker"])
	}
	if p["volume"] != 30 || p["muted"] != false {
		t.Fatalf("volume/muted=%v/%v", p["volume"], p["muted"])
	}
	if _, ok := p["poll"]; !ok {
		t.Fatal("缺 poll 段")
	}
	q, ok := p["queue"].(queue.QueueView)
	if !ok || q.Repeat != "list" || !q.Playing {
		t.Fatalf("queue 段不对：%+v", p["queue"])
	}
}

func TestSigIncludesLikedAndVolume(t *testing.T) {
	a, s := newEventSession()
	ch, cancel := a.hub.subscribe()
	defer cancel()
	s.emitStateIfChanged()
	a.likedMu.Lock()
	a.liked = map[int64]bool{1: true}
	a.likedMu.Unlock()
	s.emitStateIfChanged() // 收藏状态变了 → 必须推一帧
	waitFor(t, ch, "state")
	n := 0
	for i := 0; i < 5; i++ {
		s.emitStateIfChanged()
	}
	select {
	case <-ch:
		n++
	default:
	}
	if n != 0 {
		t.Fatal("签名没变不该重复推送")
	}
}

func TestSigBriefIsReadable(t *testing.T) {
	_, s := newEventSession()
	s.emitStateIfChanged()
	s.q.MarkPlaying()
	s.emitStateIfChanged()
	if got := sigBrief(s.lastSig); got == s.lastSig || got[:5] != "song=" {
		t.Fatalf("sigBrief 应输出人话，得到 %q", got)
	}
}

func TestPollDebugOffForNoop(t *testing.T) {
	_, s := newEventSession() // 没 initPoll：steady=0、wakePoll=nil
	if m := s.pollDebug()["mode"]; m != "off" {
		t.Fatalf("noop 音箱的 poll 模式应是 off，得到 %v", m)
	}
	s.markUnstable("测试") // 应当安静地什么都不做
	if s.poll.left != 0 {
		t.Fatal("关闭回读时不该有 burst 额度")
	}
}
