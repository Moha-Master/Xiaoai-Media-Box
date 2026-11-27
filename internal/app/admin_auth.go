package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/qr"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/xiaomi"
)

func (a *App) getCloudClient() *xiaomi.Client {
	if a.cloud != nil {
		return a.cloud
	}
	a.cloud = xiaomi.NewClient(a.cfg.Xiaomi)
	return a.cloud
}

func (a *App) hAdminXiaomiAuthStatus(w http.ResponseWriter, r *http.Request) {
	st, ok, err := xiaomi.LoadState(a.cfg.Xiaomi.TokenFile, a.cfg.Xiaomi.KeyFile)
	if err != nil {
		writeErr(w, 500, fmt.Errorf("读取小米认证状态失败：%w", err))
		return
	}
	out := map[string]any{
		"ok":         true,
		"logged_in":  ok && (st.PassToken != "" || st.ServiceToken != ""),
		"token_file": a.cfg.Xiaomi.TokenFile,
	}
	if ok {
		out["user_id"] = st.UserID
		out["device_id"] = st.DeviceID
		out["updated_at"] = st.UpdatedAt
	}
	writeJSON(w, 200, out)
}

func (a *App) hAdminXiaomiAuthLogout(w http.ResponseWriter, r *http.Request) {
	_ = xiaomi.ClearState(a.cfg.Xiaomi.TokenFile, a.cfg.Xiaomi.KeyFile)
	a.cloud = nil
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) hAdminXiaomiQRStart(w http.ResponseWriter, r *http.Request) {
	sess, err := xiaomi.DefaultQRManager.Start(r.Context(), a.cfg.Xiaomi)
	if err != nil {
		writeErr(w, 500, fmt.Errorf("启动扫码登录失败：%w", err))
		return
	}
	png, err := qr.PNG(sess.LoginURL)
	if err != nil {
		writeErr(w, 500, fmt.Errorf("生成二维码图片失败：%w", err))
		return
	}
	qrData := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	writeJSON(w, 200, map[string]any{
		"ok":         true,
		"session_id": sess.ID,
		"login_url":  sess.LoginURL,
		"qr":         qrData,
		"timeout":    sess.Timeout,
	})
}

func (a *App) hAdminXiaomiQRCheck(w http.ResponseWriter, r *http.Request) {
	sessID := r.URL.Query().Get("session_id")
	if sessID == "" {
		writeErr(w, 400, errors.New("缺少 session_id"))
		return
	}
	sess, ok := xiaomi.DefaultQRManager.Get(sessID)
	if !ok {
		writeErr(w, 404, errors.New("会话不存在或已过期"))
		return
	}
	if sess.Status == xiaomi.QRStatusAuthorized {
		a.cloud = xiaomi.NewClient(a.cfg.Xiaomi)
	}
	writeJSON(w, 200, map[string]any{
		"ok":      true,
		"status":  sess.Status,
		"user_id": sess.UserID,
		"error":   sess.Error,
	})
}

func (a *App) hAdminXiaomiDevices(w http.ResponseWriter, r *http.Request) {
	cloud := a.getCloudClient()
	devs, err := cloud.DeviceList(r.Context())
	if err != nil {
		writeErr(w, 502, fmt.Errorf("获取小米音箱设备列表失败：%w", err))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "devices": devs})
}

func (a *App) hAdminXiaomiCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if err := xiaomi.CheckAuth(ctx, a.cfg.Xiaomi); err != nil {
		writeErr(w, 400, err)
		return
	}
	cloud := a.getCloudClient()
	devs, _ := cloud.DeviceList(ctx)
	writeJSON(w, 200, map[string]any{
		"ok":      true,
		"message": "小米凭据体检通过，token 续期正常",
		"devices": devs,
	})
}
