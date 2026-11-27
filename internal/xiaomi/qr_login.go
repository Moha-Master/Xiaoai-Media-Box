package xiaomi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/auth"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/util"
)

type QRStatus string

const (
	QRStatusWaiting    QRStatus = "waiting"
	QRStatusAuthorized QRStatus = "authorized"
	QRStatusExpired    QRStatus = "expired"
	QRStatusFailed     QRStatus = "failed"
)

// QRSession 是网页端扫码登录的会话状态。
type QRSession struct {
	ID        string    `json:"session_id"`
	LoginURL  string    `json:"login_url"`
	Timeout   int       `json:"timeout"`
	Status    QRStatus  `json:"status"`
	UserID    string    `json:"user_id,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	mu     sync.Mutex
	cancel context.CancelFunc
}

func (s *QRSession) snapshot() QRSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return QRSession{
		ID:        s.ID,
		LoginURL:  s.LoginURL,
		Timeout:   s.Timeout,
		Status:    s.Status,
		UserID:    s.UserID,
		Error:     s.Error,
		CreatedAt: s.CreatedAt,
	}
}

type QRManager struct {
	mu       sync.Mutex
	sessions map[string]*QRSession
}

var DefaultQRManager = NewQRManager()

func NewQRManager() *QRManager {
	return &QRManager{sessions: map[string]*QRSession{}}
}

func genSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Start 开始一次分段式网页扫码登录。
func (m *QRManager) Start(parentCtx context.Context, cfg config.XiaomiConfig) (*QRSession, error) {
	statePath, keyPath := cfg.TokenFile, cfg.KeyFile
	if statePath == "" {
		return nil, errors.New("配置里缺少 xiaomi.token_file")
	}
	if keyPath == "" {
		keyPath = statePath + ".key"
	}
	if err := auth.CheckWritable(statePath, keyPath); err != nil {
		return nil, err
	}

	deviceID := cfg.DeviceID
	if st, ok, err := loadXiaomiState(statePath, keyPath); err == nil && ok && deviceID == "" {
		deviceID = st.DeviceID
	}
	if deviceID == "" {
		deviceID = randomDeviceID()
	}

	s := newAuthSession()
	s.setCookieAll("sdkVersion", "accountsdk-18.8.15")
	s.setCookieAll("deviceId", deviceID)
	q := url.Values{
		"_qrsize":      {"480"},
		"qs":           {fmt.Sprintf("?sid=%s&_json=true", micoapiSID)},
		"callback":     {stsCallback},
		"_hasLogo":     {"false"},
		"sid":          {micoapiSID},
		"serviceParam": {""},
		"_locale":      {"zh_CN"},
		"_dc":          {strconv.FormatInt(time.Now().UnixMilli(), 10)},
	}

	d, status, err := s.get(parentCtx, minaAccount+"/longPolling/loginUrl?"+q.Encode())
	if err != nil {
		return nil, err
	}
	loginURL, lp := strOf(d["loginUrl"]), strOf(d["lp"])
	if loginURL == "" || lp == "" {
		return nil, fmt.Errorf("拿不到登录链接（HTTP %d, code=%v）", status, d["code"])
	}
	timeout := util.AsInt(d["timeout"])
	if timeout <= 0 {
		timeout = 300
	}

	sessID := genSessionID()
	bgCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	sess := &QRSession{
		ID:        sessID,
		LoginURL:  loginURL,
		Timeout:   timeout,
		Status:    QRStatusWaiting,
		CreatedAt: time.Now(),
		cancel:    cancel,
	}

	m.mu.Lock()
	// 清理 10 分钟以前的过期会话
	now := time.Now()
	for k, v := range m.sessions {
		if now.Sub(v.CreatedAt) > 10*time.Minute {
			delete(m.sessions, k)
		}
	}
	m.sessions[sessID] = sess
	m.mu.Unlock()

	// 后台轮询 lp，等待用户在手机或浏览器上授权
	go func() {
		defer cancel()
		var info map[string]any
		deadline := time.Now().Add(time.Duration(timeout) * time.Second)

		for time.Now().Before(deadline) && bgCtx.Err() == nil {
			d, st, err := s.getTimeout(bgCtx, lp, 20*time.Second)
			if err != nil {
				if errors.Is(bgCtx.Err(), context.Canceled) {
					return
				}
				continue // 长轮询超时是正常的，继续等
			}
			if st != http.StatusOK {
				continue
			}
			if strOf(d["location"]) != "" {
				info = d
				break
			}
		}

		sess.mu.Lock()
		defer sess.mu.Unlock()

		if info == nil {
			if errors.Is(bgCtx.Err(), context.Canceled) {
				sess.Status = QRStatusFailed
				sess.Error = "会话已取消"
			} else {
				sess.Status = QRStatusExpired
				sess.Error = "等待超时，二维码已过期"
			}
			return
		}

		st := State{
			DeviceID:  deviceID,
			UserID:    strOf(info["userId"]),
			PassToken: strOf(info["passToken"]),
		}
		if st.PassToken == "" {
			sess.Status = QRStatusFailed
			sess.Error = "授权成功但未拿到 passToken"
			return
		}

		token, passToken, userID, err := fetchServiceToken(bgCtx, st.UserID, deviceID, st.PassToken)
		if err != nil {
			if tok2, err2 := s.finishLocation(bgCtx, strOf(info["location"])); err2 == nil {
				token = tok2
			} else {
				sess.Status = QRStatusFailed
				sess.Error = fmt.Sprintf("获取 serviceToken 失败：%v", err)
				return
			}
		}
		if passToken != "" {
			st.PassToken = passToken
		}
		if userID != "" {
			st.UserID = userID
		}
		st.ServiceToken = token

		if err := saveXiaomiState(statePath, keyPath, st); err != nil {
			sess.Status = QRStatusFailed
			sess.Error = fmt.Sprintf("保存凭据失败：%v", err)
			return
		}

		sess.Status = QRStatusAuthorized
		sess.UserID = st.UserID
	}()

	return sess, nil
}

// Get 查阅当前会话状态。
func (m *QRManager) Get(sessionID string) (*QRSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[sessionID]
	if !ok {
		return nil, false
	}
	snap := sess.snapshot()
	return &snap, true
}

// Cancel 取消会话。
func (m *QRManager) Cancel(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.sessions[sessionID]; ok {
		if sess.cancel != nil {
			sess.cancel()
		}
		delete(m.sessions, sessionID)
	}
}
