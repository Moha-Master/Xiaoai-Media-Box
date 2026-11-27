//go:build !linux && !darwin

package app

import "errors"

// selfExec 在非 unix 平台没有对等的「原地替换进程」语义，老老实实报错让人手工重启。
func selfExec() error {
	return errors.New("该平台不支持自重启，请手工重启服务")
}
