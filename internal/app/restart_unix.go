//go:build linux || darwin

package app

import (
	"errors"
	"os"
	"syscall"
)

// selfExec 用同名可执行文件替换当前进程。
//
// 保留 pid、fd 0/1/2 与 systemd 的 cgroup / journal 连接，所以 Type=simple 的 unit
// 完全无感；因为不是「退出」，Restart=on-failure 也不会被触发（不会多一次 30s 等待）。
func selfExec() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if len(os.Args) == 0 {
		return errors.New("argv 为空，无法自重启")
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
