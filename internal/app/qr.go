package app

import "github.com/Moha-Master/Xiaoai-Media-Box/internal/qr"

// qrPNG 把登录链接渲染成 PNG（给 WebUI 的扫码登录接口用）。
func qrPNG(s string) ([]byte, error) {
	return qr.PNG(s)
}
