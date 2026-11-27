package util

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// WriteJSONFile 把 v 缩进序列化成 JSON 并**原子**写入 path。
//
// 约定抄 `auth.SaveSealedJSON`（目录 0700、文件 0600、`path.tmp` + rename），
// 区别是不加密：播放队列、音量这类运行状态里没有秘密，加密只会多一个密钥文件。
// rename 同目录所以是原子的——进程被 kill 在中间也不会留下半截 JSON。
func WriteJSONFile(path string, v any) error {
	plain, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, plain, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ReadJSONFile 读一个 JSON 文件。文件不存在或为空返回 (false, nil)，
// 调用方只需要区分「没有快照」和「快照坏了」。
func ReadJSONFile(path string, v any) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if len(b) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, err
	}
	return true, nil
}
