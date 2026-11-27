package app

import (
	"log"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
)

// 红心（喜欢）状态与歌词缓存。
//
// 两者都是“按歌 id 查外部 API”的只读数据，放在 App 层共享
// （同一首歌在两台音箱上红心状态当然一致）。
//
// **没有轮询、没有 TTL、没有覆盖表**：
//   - 只有在“需要知道当前这首歌的收藏状态”时（= 换曲）才拉一次喜欢列表；
//   - 点红心是本地写操作，直接改缓存，立刻可见；
//   - 拉列表这件事跟“播放进度”无关，不需要定时器——换曲自然会带来一次判定。
//
// 副作用：如果你在网易云手机端改了收藏，本服务要到下一次换曲（或重启）才会发现。
// 一个极小的竞态：并行的 loadLiked 可能用稍早的服务端快照盖掉刚成功的 SetLiked，
// 下一次换曲自愈，不值得为此加复杂度。

const (
	lyricTTL   = 30 * time.Minute
	likedRetry = 60 * time.Second // 拉取失败后的重试间隔（避免每 tick 都撞 API）
)

type lyricEntry struct {
	lines []netease.LyricLine
	at    time.Time
}

// Liked 只是缓存查表，绝不阻塞调用方。
func (a *App) Liked(songID int64) bool {
	a.likedMu.Lock()
	_, hit := a.liked[songID]
	a.likedMu.Unlock()
	return hit
}

// EnsureLiked 为某首歌准备收藏状态缓存（同一首只拉一次）。
func (a *App) EnsureLiked(songID int64) {
	if songID == 0 || a.ne == nil { // 没接网易云（noop / 单测）就别费劲
		return
	}
	a.likedMu.Lock()
	need := a.liked == nil || a.likedSong != songID
	if !a.likedErrAt.IsZero() && time.Since(a.likedErrAt) < likedRetry {
		need = false // 刚失败过，别每次都重试；下次换曲再说
	}
	if need && !a.likedBusy {
		a.likedBusy = true
		a.likedSong = songID
		go a.loadLiked()
	}
	a.likedMu.Unlock()
}

func (a *App) loadLiked() {
	var (
		ids []int64
		err error
	)
	uid := a.uid()
	if uid == 0 {
		log.Printf("红心列表跳过：网易云未登录")
	} else {
		ids, err = a.ne.LikedIDs(uid)
	}
	a.likedMu.Lock()
	defer a.likedMu.Unlock()
	a.likedBusy = false
	if err != nil {
		log.Printf("刷新红心列表失败：%v", err)
		a.likedErrAt = time.Now()
		a.likedSong = 0 // 下次换曲再试
		return
	}
	set := make(map[int64]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	if a.liked != nil {
		// 保留这次拉取之后本地刚改过的记录（见文件头的竞态说明）
		for id, v := range a.liked {
			if _, ok := set[id]; !ok {
				delete(a.liked, id)
			}
			set[id] = v
		}
	}
	a.liked = set
	log.Printf("红心列表已加载：%d 首", len(ids))
}

// SetLiked 收藏 / 取消收藏，成功后直接更新缓存。
func (a *App) SetLiked(songID int64, like bool) error {
	if err := a.ne.Like(songID, like); err != nil {
		return err
	}
	a.likedMu.Lock()
	if a.liked == nil {
		a.liked = map[int64]bool{}
	}
	if like {
		a.liked[songID] = true
	} else {
		delete(a.liked, songID)
	}
	a.likedSong = songID
	a.likedMu.Unlock()
	log.Printf("红心：song=%d like=%v", songID, like)
	return nil
}

// uid 拿当前登录用户 id（缓存，失败返回 0）。
func (a *App) uid() int64 {
	a.likedMu.Lock()
	uid := a.uidCache
	a.likedMu.Unlock()
	if uid != 0 {
		return uid
	}
	id, _, err := a.ne.Account()
	if err != nil {
		return 0
	}
	a.likedMu.Lock()
	a.uidCache = id
	a.likedMu.Unlock()
	return id
}

// LyricFor 返回某首歌解析好的歌词（带缓存；无歌词返回 nil）。
func (a *App) LyricFor(songID int64) []netease.LyricLine {
	a.lyrMu.Lock()
	if e, ok := a.lyrics[songID]; ok && time.Since(e.at) < lyricTTL {
		lines := e.lines
		a.lyrMu.Unlock()
		return lines
	}
	a.lyrMu.Unlock()

	lines, err := a.ne.Lyric(songID)
	if err != nil {
		log.Printf("拉歌词失败 song=%d：%v", songID, err)
	}
	a.lyrMu.Lock()
	if a.lyrics == nil {
		a.lyrics = map[int64]lyricEntry{}
	}
	// 简单容量控制：超了直接清空重来，够用
	if len(a.lyrics) > 200 {
		a.lyrics = map[int64]lyricEntry{}
	}
	// 失败不缓存 30 分钟：否则一次间歇性故障（换曲/重开页面都会命中空缓存），
	// 用户看到的就是「什么歌都暂无歌词」。下次换曲/查询会重试。
	if err != nil && len(lines) == 0 {
		a.lyrMu.Unlock()
		return nil
	}
	a.lyrics[songID] = lyricEntry{lines: lines, at: time.Now()}
	a.lyrMu.Unlock()
	return lines
}

// lyricSnapshot 是给 HA 歌词实体用的最小视图。
type lyricSnapshot struct {
	SongID     int64  `json:"song_id"`
	Line       string `json:"line"`
	NextLine   string `json:"next_line,omitempty"`
	Index      int    `json:"index"`
	Total      int    `json:"total"`
	PositionMs int    `json:"position_ms"`
}

// lyricAt 按当前进度挑出正在唱的那一行。
func lyricAt(lines []netease.LyricLine, posMs int) (idx int, cur, next string) {
	idx = -1
	for i, l := range lines {
		if l.T <= posMs {
			idx = i
		} else {
			break
		}
	}
	if idx >= 0 {
		cur = lines[idx].Text
		if idx+1 < len(lines) {
			next = lines[idx+1].Text
		}
	}
	return idx, cur, next
}
