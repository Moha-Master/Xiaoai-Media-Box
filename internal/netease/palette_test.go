package netease

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// ---------- 推荐类解析归一化（构造 JSON，不联网） ----------

func TestParseRecommendPlaylists(t *testing.T) {
	body := []byte(`{"code":200,"recommend":[
		{"id":1,"name":"日推一","picUrl":"http://x/1.jpg","playcount":123,"trackCount":30,"copywriter":"根据你的口味"},
		{"id":2,"name":"日推二","picUrl":"","playCount":999,"trackCount":50}
	]}`)
	pls, err := parseRecommendPlaylists(body)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(pls) != 2 {
		t.Fatalf("应得 2 条，得到 %d", len(pls))
	}
	p := pls[0]
	if p.ID != 1 || p.Name != "日推一" || p.Cover != "http://x/1.jpg" || p.PlayCount != 123 || p.TrackCount != 30 || p.Copywriter != "根据你的口味" {
		t.Fatalf("字段映射不对：%+v", p)
	}
	// playcount 全小写 / playCount 驼峰都能对上（json 大小写不敏感）
	if pls[1].PlayCount != 999 {
		t.Fatalf("驼峰 playCount 没对上：%+v", pls[1])
	}
}

func TestParsePersonalizedPlaylists(t *testing.T) {
	// playCount 用科学计数法：上游对大数会这么打（实测 2.55e10），int64 解不了
	body := []byte(`{"code":200,"result":[
		{"id":11,"name":"专属歌单","picUrl":"http://x/p.jpg","playCount":2.55355658E10,"trackCount":5,"copywriter":"编辑推荐","alg":"featured"}
	]}`)
	pls, err := parsePersonalizedPlaylists(body)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(pls) != 1 || pls[0].ID != 11 || pls[0].TrackCount != 5 {
		t.Fatalf("字段映射不对：%+v", pls)
	}
	if pls[0].PlayCount != 25535565800 {
		t.Fatalf("科学计数法 playCount 没对上：%v", pls[0].PlayCount)
	}
}

func TestParseToplists(t *testing.T) {
	body := []byte(`{"code":200,"list":[
		{"id":19723756,"name":"飙升榜","coverImgUrl":"http://x/c.jpg","playCount":456,"ToplistType":"云音乐飙升榜"}
	]}`)
	list, err := parseToplists(body)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(list) != 1 || list[0].ID != 19723756 || list[0].Type != "云音乐飙升榜" || list[0].PlayCount != 456 {
		t.Fatalf("字段映射不对：%+v", list)
	}
}

func TestParseNewAlbums(t *testing.T) {
	// 形态一：artist 单对象
	body := []byte(`{"code":200,"albums":[
		{"id":1,"name":"专辑A","picUrl":"http://x/a.jpg","artist":{"id":9,"name":"歌手A"}}
	]}`)
	albums, err := parseNewAlbums(body)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(albums) != 1 || albums[0].Artist != "歌手A" {
		t.Fatalf("artist 单对象没对上：%+v", albums)
	}
	// 形态二：artists 数组
	body = []byte(`{"code":200,"albums":[
		{"id":2,"name":"专辑B","artists":[{"name":"歌手B1"},{"name":"歌手B2"}]}
	]}`)
	albums, err = parseNewAlbums(body)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if albums[0].Artist != "歌手B1/歌手B2" {
		t.Fatalf("artists 数组没对上：%+v", albums)
	}
}

func TestParseHotSearches(t *testing.T) {
	body := []byte(`{"code":200,"result":{"hots":[{"first":"热词1","second":"x"},{"first":""},{"first":"热词2"}]}}`)
	hots, err := parseHotSearches(body)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(hots) != 2 || hots[0] != "热词1" || hots[1] != "热词2" {
		t.Fatalf("应过滤空词：%+v", hots)
	}
}

func TestParseFM(t *testing.T) {
	// 形态一：mainSong 包一层
	body := []byte(`{"code":200,"data":[
		{"id":1,"type":0,"mainSong":{"id":111,"name":"歌A","dt":200000,
			"ar":[{"name":"歌手A"}],"al":{"name":"专辑A","picUrl":"http://x/f.jpg"}}}
	]}`)
	tracks, err := parseFM(body)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(tracks) != 1 || tracks[0].ID != 111 || tracks[0].Duration != 200000 ||
		tracks[0].Artists != "歌手A" || tracks[0].Cover != "http://x/f.jpg" {
		t.Fatalf("mainSong 形态没对上：%+v", tracks)
	}
	// 形态二：平铺在 data 项上
	body = []byte(`{"code":200,"data":[
		{"id":222,"name":"歌B","dt":300000,"ar":[{"name":"歌手B"}],"al":{"name":"专辑B"}}
	]}`)
	tracks, err = parseFM(body)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(tracks) != 1 || tracks[0].ID != 222 || tracks[0].Name != "歌B" {
		t.Fatalf("平铺形态没对上：%+v", tracks)
	}
	// 平铺 id=0 且无 mainSong → 跳过
	body = []byte(`{"code":200,"data":[{"id":0,"type":0}]}`)
	tracks, err = parseFM(body)
	if err != nil || len(tracks) != 0 {
		t.Fatalf("空项应跳过：%+v err=%v", tracks, err)
	}
}

func TestParseSearchResultSong(t *testing.T) {
	body := []byte(`{"code":200,"result":{"songs":[
		{"id":1,"name":"晴天","dt":269000,"ar":[{"name":"周杰伦"}],"al":{"name":"叶惠美","picUrl":"http://x/s.jpg"}}
	]}}`)
	res, err := parseSearchResult(body, "song")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(res.Tracks) != 1 || res.Tracks[0].Label() != "周杰伦 - 晴天" {
		t.Fatalf("song 归一化不对：%+v", res.Tracks)
	}
	if res.Playlists != nil || res.Artists != nil || res.Albums != nil {
		t.Fatalf("其它切片应为空：%+v", res)
	}
}

func TestParseSearchResultOtherTypes(t *testing.T) {
	// playlist
	body := []byte(`{"code":200,"result":{"playlists":[
		{"id":100,"name":"歌单X","coverImgUrl":"http://x/l.jpg","playCount":77,"trackCount":22}
	]}}`)
	res, err := parseSearchResult(body, "playlist")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Playlists) != 1 || res.Playlists[0].PlayCount != 77 || res.Playlists[0].Cover != "http://x/l.jpg" {
		t.Fatalf("playlist 没对上：%+v", res.Playlists)
	}
	// artist
	body = []byte(`{"code":200,"result":{"artists":[
		{"id":200,"name":"歌手X","picUrl":"http://x/ar.jpg","alias":["X兄"],"musicSize":300,"albumSize":40}
	]}}`)
	res, err = parseSearchResult(body, "artist")
	if err != nil {
		t.Fatal(err)
	}
	a := res.Artists[0]
	if a.ID != 200 || a.MusicSize != 300 || a.AlbumSize != 40 || len(a.Alias) != 1 || a.Alias[0] != "X兄" {
		t.Fatalf("artist 没对上：%+v", a)
	}
	// album
	body = []byte(`{"code":200,"result":{"albums":[
		{"id":300,"name":"专辑X","picUrl":"http://x/al.jpg","songsCount":12,"publishTime":1122334455,"artist":{"name":"歌手X"}}
	]}}`)
	res, err = parseSearchResult(body, "album")
	if err != nil {
		t.Fatal(err)
	}
	al := res.Albums[0]
	if al.ID != 300 || al.SongsCount != 12 || al.PublishTime != 1122334455 || al.Artist != "歌手X" {
		t.Fatalf("album 没对上：%+v", al)
	}
}

// ---------- 取色（内存构造图片，不联网） ----------

func gradientPNG(t *testing.T, w, h int, fn func(x, y float64) color.RGBA) image.Image {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, fn(float64(x)/float64(w-1), float64(y)/float64(h-1)))
		}
	}
	return img
}

func TestPaletteFromImage(t *testing.T) {
	// 上半鲜艳的蓝、下半暗红：vibrant 应偏向亮蓝，dark 应偏向暗红
	img := gradientPNG(t, 64, 64, func(x, y float64) color.RGBA {
		if y < 0.5 {
			return color.RGBA{R: 60, G: 140, B: 240, A: 255}
		}
		return color.RGBA{R: 90, G: 20, B: 20, A: 255}
	})
	pal := paletteFromImage(img)
	for _, hex := range []string{pal.Vibrant, pal.Dark, pal.Muted} {
		if len(hex) != 7 || hex[0] != '#' {
			t.Fatalf("颜色不是 #rrggbb：%q (%+v)", hex, pal)
		}
	}
	if pal.Vibrant != "#3c8cf0" { // 60/140/240
		t.Fatalf("vibrant 应为亮蓝 #3c8cf0，得到 %s", pal.Vibrant)
	}
	if pal.Dark != "#5a1414" { // 90/20/20
		t.Fatalf("dark 应为暗红 #5a1414，得到 %s", pal.Dark)
	}
}

func TestPaletteFromImageMonochrome(t *testing.T) {
	// 纯色图：三种主色都退化到同一块，但永远是合法 #rrggbb、不为空
	img := gradientPNG(t, 32, 32, func(x, y float64) color.RGBA {
		return color.RGBA{R: 200, G: 30, B: 90, A: 255}
	})
	pal := paletteFromImage(img)
	for name, hex := range map[string]string{"vibrant": pal.Vibrant, "dark": pal.Dark, "muted": pal.Muted} {
		if len(hex) != 7 || hex[0] != '#' {
			t.Fatalf("%s 非法：%q", name, hex)
		}
	}
}

func TestShrink(t *testing.T) {
	img := gradientPNG(t, 300, 150, func(x, y float64) color.RGBA { return color.RGBA{A: 255} })
	s := shrink(img, 96)
	b := s.Bounds()
	if b.Dx() != 96 || b.Dy() != 48 { // 宽高比 2:1 保持
		t.Fatalf("缩放比例不对：%dx%d", b.Dx(), b.Dy())
	}
	small := gradientPNG(t, 50, 50, func(x, y float64) color.RGBA { return color.RGBA{A: 255} })
	if s2 := shrink(small, 96); s2.Bounds().Dx() != 50 {
		t.Fatalf("小图不该被放大")
	}
}

func TestRGBToHSLAndHex(t *testing.T) {
	// 纯红：h=0, s=1, l=0.5
	h, s, l := rgbToHSL(255, 0, 0)
	if h != 0 || s != 1 || l != 0.5 {
		t.Fatalf("纯红 HSL 不对：%v %v %v", h, s, l)
	}
	// 灰色：s=0
	if _, s, _ := rgbToHSL(128, 128, 128); s != 0 {
		t.Fatalf("灰色饱和度应为 0，得到 %v", s)
	}
	if got := rgbHex(60.4, 140.6, 240.0); got != "#3c8df0" { // 60→0x3c, 140.6 四舍五入 141→0x8d
		t.Fatalf("rgbHex 不对：%s", got)
	}
	if got := rgbHex(300, -5, 128.5); got != "#ff0081" {
		t.Fatalf("rgbHex 越界钳制不对：%s", got)
	}
}

func TestCoverPaletteRejectsEmpty(t *testing.T) {
	if _, err := CoverPalette(""); err == nil {
		t.Fatal("空 URL 应报错")
	}
}

// 顺带验证：png 解码器已注册（paletteFromImage 的输入常来自 image.Decode）。
func TestPNGDecodeRegistered(t *testing.T) {
	img := gradientPNG(t, 8, 8, func(x, y float64) color.RGBA { return color.RGBA{R: 1, G: 2, B: 3, A: 255} })
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	got, format, err := image.Decode(&buf)
	if err != nil {
		t.Fatalf("png 解码失败：%v", err)
	}
	if format != "png" {
		t.Fatalf("format=%s", format)
	}
	if got.Bounds().Dx() != 8 {
		t.Fatalf("尺寸不对")
	}
}
