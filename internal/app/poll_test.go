package app

import (
	"testing"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
)

func newPollSession(steady, burst time.Duration, rounds int) *Session {
	s := &Session{
		speaker:  config.SpeakerConfig{ID: "t", Name: "t", StatusPoll: 60},
		wakePoll: make(chan struct{}, 1),
	}
	s.pol = pollPolicy{steady: steady, burst: burst, rounds: rounds, drift: 4 * time.Second}
	return s
}

func TestPolicyDefaults(t *testing.T) {
	ssh := policyFromConfig(config.SpeakerConfig{Control: "ssh", StatusPoll: 15})
	if ssh.burst != 5*time.Second || ssh.rounds != 3 || ssh.drift != 4*time.Second {
		t.Fatalf("ssh 默认值不对：%+v", ssh)
	}
	cloud := policyFromConfig(config.SpeakerConfig{Control: "cloud", StatusPoll: 60})
	if cloud.drift != 8*time.Second {
		t.Fatalf("cloud 的偏差阈值应放宽到 8s（MiNA 起播延迟大），得到 %v", cloud.drift)
	}
	// 加密间隔比稳态还长 → 没有意义，压平
	p2 := policyFromConfig(config.SpeakerConfig{StatusPoll: 3, PollBurst: 30, PollRounds: 5})
	if p2.burst != 3*time.Second {
		t.Fatalf("burst 应被压到 steady，得到 %v", p2.burst)
	}
}

func TestBurstThenSteady(t *testing.T) {
	s := newPollSession(60*time.Second, 5*time.Second, 3)
	s.markUnstable("测试操作")
	for i, want := range []time.Duration{5 * time.Second, 5 * time.Second, 5 * time.Second, 60 * time.Second} {
		if got := s.consumeDelay(); got != want {
			t.Fatalf("第 %d 次应等 %v，得到 %v", i+1, want, got)
		}
	}
	if d := s.pollDebug()["mode"]; d != "steady" {
		t.Fatalf("额度用完后应回稳态，得到 %v", d)
	}
}

func TestBurstDoesNotStack(t *testing.T) {
	s := newPollSession(60*time.Second, 5*time.Second, 3)
	for i := 0; i < 10; i++ {
		s.markUnstable("连续操作")
	}
	if left := s.poll.left; left != 3 {
		t.Fatalf("重复触发应锁定在 3 轮，得到 %d", left)
	}
}

func TestProbeFailureOnlyBurstsOnRecovery(t *testing.T) {
	s := newPollSession(60*time.Second, 5*time.Second, 3)
	s.noteProbeResult(errFake)
	s.noteProbeResult(errFake)
	if s.poll.left != 0 {
		t.Fatalf("连续失败不该加密回读（会一直撞云端），得到 %d", s.poll.left)
	}
	s.noteProbeResult(nil)
	if s.poll.left != 3 {
		t.Fatalf("失败→恢复应进入不稳定态，得到 %d", s.poll.left)
	}
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "boom" }
