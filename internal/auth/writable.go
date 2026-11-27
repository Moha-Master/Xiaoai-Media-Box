package auth

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

// CheckWritable 在开始登录之前确认认证文件和密钥文件可写。
//
// 认证文件默认 0600、属主是跑 daemon 的那个用户。用另一个用户跑 `-login`
// 会撞 EACCES，而那一刻往往已经扫码/输验证码完成了，体验极差。
func CheckWritable(statePath, keyPath string) error {
	dir := filepath.Dir(statePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("无法创建认证目录 %s：%w", dir, err)
	}
	who := "当前用户"
	if u, err := user.Current(); err == nil {
		who = fmt.Sprintf("%s(uid=%s)", u.Username, u.Uid)
	}
	for _, p := range []string{statePath, keyPath} {
		if _, err := os.Stat(p); err == nil {
			f, err := os.OpenFile(p, os.O_WRONLY, 0)
			if err != nil {
				return fmt.Errorf("没有写权限：%s（%v）。\n认证文件应归运行 daemon 的用户所有；当前是 %s。\n  要么用同一个用户运行它，要么：\n    sudo chown %s %s %s",
					p, err, who, usernameOr("(当前用户)"), statePath, keyPath)
			}
			f.Close()
			continue
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("无法创建 %s（%v）；当前是 %s，请检查目录权限或换用户运行", p, err, who)
		}
		f.Close()
	}
	return nil
}

func usernameOr(fallback string) string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return fallback
}
