// Package qr 负责终端二维码和 PNG 渲染。
package qr

import (
	"fmt"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// Print 在终端画一个二维码（真机登录用）。
func Print(s string) error {
	q, err := qrcode.New(s, qrcode.Medium)
	if err != nil {
		return err
	}
	bm := q.Bitmap()
	for _, row := range bm {
		var b strings.Builder
		b.Grow(len(row) * 3)
		for _, on := range row {
			if on {
				b.WriteString("██")
			} else {
				b.WriteString("  ")
			}
		}
		fmt.Println(b.String())
	}
	return nil
}

// PNG 把内容渲染成二维码 PNG。
func PNG(s string) ([]byte, error) {
	return qrcode.Encode(s, qrcode.Medium, 400)
}
