package netease

import (
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // 注册解码器：封面基本是 jpeg/png，gif 兜底
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"time"
)

// Palette 是封面取色的结果，全部 #rrggbb（与 WebUI 直接拼 CSS 兼容）。
//
// 注：任务书允许用 github.com/muesli/go-colorful，实测该 fork（2017 年最后
// 提交）只有从种子生成随机颜色的算法，没有 MakePalette/Vibrant 这类从图片
// 提取颜色的 API，所以这里退化为手写「中位切分 + 评分挑选」，见下方
// paletteFromImage，约 80 行、零第三方依赖。
type Palette struct {
	Vibrant string `json:"vibrant"`
	Dark    string `json:"dark"`
	Muted   string `json:"muted"`
}

// paletteHTTP 独立于业务 HTTP 客户端：取封面只要 10 秒超时，且不需要 cookie jar。
var paletteHTTP = &http.Client{Timeout: 10 * time.Second}

// coverMaxBytes 封面下载上限。专辑封面 300x300 的 jpeg 通常几十 KB，
// 4MB 已经覆盖到超大封面，超出直接按取色失败处理。
const coverMaxBytes = 4 << 20

// CoverPalette 下载封面并提取 vibrant / dark / muted 三个主色。
// 网络、解码、取色任一步失败都返回 error，由 HTTP 层兜底默认调色板。
func CoverPalette(coverURL string) (Palette, error) {
	if coverURL == "" {
		return Palette{}, fmt.Errorf("没有封面地址")
	}
	// 网易云封面 URL 不带 param 时返回**原图**（实测有 7MB 的演唱会图，
	// 超限截断后 JPEG 解码直接失败）；取色只需要 400px，缩略参数把体积
	// 砍到 ~75KB，还顺带把耗时从秒级降到百毫秒级。
	if !strings.Contains(coverURL, "param=") {
		coverURL += "?param=400y400"
	}
	resp, err := paletteHTTP.Get(coverURL)
	if err != nil {
		return Palette{}, fmt.Errorf("封面下载失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Palette{}, fmt.Errorf("封面下载失败 status=%d", resp.StatusCode)
	}
	img, _, err := image.Decode(io.LimitReader(resp.Body, coverMaxBytes))
	if err != nil {
		return Palette{}, fmt.Errorf("封面解码失败：%w", err)
	}
	return paletteFromImage(img), nil
}

// bucket 是中位切分的一个色块：范围 + 平均色。
type bucket struct {
	r, g, b  int64 // 成员之和（求平均用）
	n        int64
	min, max [3]uint8
}

func (b *bucket) mean() (r, g, b2 float64) {
	return float64(b.r) / float64(b.n), float64(b.g) / float64(b.n), float64(b.b) / float64(b.n)
}

// paletteFromImage 对图片做中位切分（8 块），再按饱和度/亮度给每块打分挑主色。
// 三种主色各有兜底：图片是纯色时也会退化到最接近的那一块，永不返回空。
func paletteFromImage(img image.Image) Palette {
	sm := shrink(img, 96) // 96x96 足够取色，省得大图逐像素算
	b := sm.Bounds()

	px := make([][3]uint8, 0, 96*96)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bb, _ := sm.At(x, y).RGBA() // 0-65535，取高 8 位
			px = append(px, [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(bb >> 8)})
		}
	}
	if len(px) == 0 {
		return Palette{Vibrant: "#4ea1ff", Dark: "#1a2433", Muted: "#2a3542"}
	}

	cuts := medianCut(px, 8)
	best := func(score func(h, s, l float64) float64) string {
		bestScore := -1.0
		hex := ""
		for _, bk := range cuts {
			r, g, bl := bk.mean()
			h, s, l := rgbToHSL(r, g, bl)
			if sc := score(h, s, l); sc > bestScore {
				bestScore = sc
				hex = rgbHex(r, g, bl)
			}
		}
		return hex
	}
	// vibrant：饱和度高、亮度在中间偏高（纯黑纯白都算不上「鲜艳」）
	vibrant := best(func(h, s, l float64) float64 {
		return s * (1 - 1.6*abs(l-0.55))
	})
	// dark：偏暗的那块里挑饱和度最高的，保证不是死黑
	dark := best(func(h, s, l float64) float64 {
		if l > 0.42 {
			return 0
		}
		return s + 0.15*(0.42-l)
	})
	// muted：低饱和、亮度居中（做背景/次级文字色）
	muted := best(func(h, s, l float64) float64 {
		return (1 - abs(s-0.18)*2) * (1 - abs(l-0.42)*1.5)
	})
	return Palette{Vibrant: vibrant, Dark: dark, Muted: muted}
}

// medianCut 经典中位切分：反复把像素数最多的块沿最长的颜色轴对半劈。
func medianCut(px [][3]uint8, n int) []bucket {
	buckets := []bucket{newBucket(px)}
	for len(buckets) < n {
		// 找像素最多的块
		bi := -1
		for i := range buckets {
			if bi < 0 || buckets[i].n > buckets[bi].n {
				bi = i
			}
		}
		bk := buckets[bi]
		if bk.n < 2 {
			break // 已经劈不动了
		}
		// 找跨度最大的颜色轴
		axis := 0
		for c := 1; c < 3; c++ {
			if bk.max[c]-bk.min[c] > bk.max[axis]-bk.min[axis] {
				axis = c
			}
		}
		mid := (int(bk.min[axis]) + int(bk.max[axis])) / 2
		var lo, hi [][3]uint8
		for _, p := range px {
			if !inBucket(bk, p) {
				continue
			}
			if int(p[axis]) <= mid {
				lo = append(lo, p)
			} else {
				hi = append(hi, p)
			}
		}
		if len(lo) == 0 || len(hi) == 0 {
			break // 该块像素全挤在一根轴上，切不开了
		}
		buckets[bi] = newBucket(lo)
		buckets = append(buckets, newBucket(hi))
	}
	return buckets
}

func newBucket(px [][3]uint8) bucket {
	var b bucket
	b.n = int64(len(px))
	b.min = [3]uint8{255, 255, 255}
	for _, p := range px {
		b.r += int64(p[0])
		b.g += int64(p[1])
		b.b += int64(p[2])
		for c := 0; c < 3; c++ {
			if p[c] < b.min[c] {
				b.min[c] = p[c]
			}
			if p[c] > b.max[c] {
				b.max[c] = p[c]
			}
		}
	}
	return b
}

func inBucket(b bucket, p [3]uint8) bool {
	for c := 0; c < 3; c++ {
		if p[c] < b.min[c] || p[c] > b.max[c] {
			return false
		}
	}
	return true
}

// shrink 最近邻缩到 max 以内：取色不需要保真，只要像素分布比例不变。
func shrink(img image.Image, max int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return img
	}
	nw, nh := w, h
	if w > h {
		nh = h * max / w
		nw = max
	} else {
		nw = w * max / h
		nh = max
	}
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := b.Min.Y + y*h/nh
		for x := 0; x < nw; x++ {
			sx := b.Min.X + x*w/nw
			out.Set(x, y, color.RGBAModel.Convert(img.At(sx, sy)))
		}
	}
	return out
}

// rgbToHSL 标准 RGB→HSL，取色评分够用（h 其实没用到，留着便于调评分公式）。
func rgbToHSL(r, g, b float64) (h, s, l float64) {
	r, g, b = r/255, g/255, b/255
	mx, mn := max3(r, g, b), min3(r, g, b)
	l = (mx + mn) / 2
	if mx == mn {
		return 0, 0, l
	}
	d := mx - mn
	if l > 0.5 {
		s = d / (2 - mx - mn)
	} else {
		s = d / (mx + mn)
	}
	switch mx {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return h / 6, s, l
}

func max3(a, b, c float64) float64 {
	if a > b {
		if a > c {
			return a
		}
		return c
	}
	if b > c {
		return b
	}
	return c
}

func min3(a, b, c float64) float64 {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func rgbHex(r, g, b float64) string {
	clamp := func(v float64) int {
		i := int(v + 0.5)
		if i < 0 {
			i = 0
		}
		if i > 255 {
			i = 255
		}
		return i
	}
	return fmt.Sprintf("#%02x%02x%02x", clamp(r), clamp(g), clamp(b))
}
