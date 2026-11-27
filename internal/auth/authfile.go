package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 认证文件是一个"本地小保险箱"：AES-256-GCM，密钥单独放一个 0600 文件。
//
// 边界要说清楚，别高估它：
//   - 防的是「认证文件被单独复制走」：备份、同步盘、误传、误提交到 git —— 没有密钥就是垃圾；
//   - 防不了能读同机密钥文件的攻击者（root 依然能拿到）；
//   - 「账号 id 当盐再 base64」是**混淆不是加密**：账号 id 是公开信息，方案又开源，等于没锁。
//
// 真要加强到"磁盘被拔走也不怕"，得上 TPM（systemd-creds）或系统 keyring——那是另一个量级，当前不做。
const Magic = "XIAOAIAUTH1"

// loadOrCreateKey 读密钥文件；不存在就生成一个新的（32 字节随机，base64 存 0600）。
func loadOrCreateKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		key, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if derr != nil || len(key) != 32 {
			return nil, fmt.Errorf("密钥文件 %s 格式不对（应为 base64 编码的 32 字节）", path)
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成密钥失败：%w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	enc := base64.StdEncoding.EncodeToString(key) + "\n"
	if err := os.WriteFile(path, []byte(enc), 0o600); err != nil {
		return nil, fmt.Errorf("写密钥文件失败：%w", err)
	}
	return key, nil
}

// sealBlob 加密：magic | nonce | ciphertext+tag。AAD 用 magic，防止密文被挪到别的用途。
func sealBlob(key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(Magic)+len(nonce)+len(plain)+gcm.Overhead())
	out = append(out, Magic...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plain, []byte(Magic)), nil
}

// openBlob 解密。没有 magic 就当成旧版明文 JSON 原样返回（向后兼容，下次保存时会自动升级）。
func openBlob(key, raw []byte) ([]byte, error) {
	if len(raw) < len(Magic) || string(raw[:len(Magic)]) != Magic {
		return raw, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < len(Magic)+gcm.NonceSize() {
		return nil, fmt.Errorf("认证文件被截断")
	}
	nonce := raw[len(Magic) : len(Magic)+gcm.NonceSize()]
	body := raw[len(Magic)+gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, body, []byte(Magic))
	if err != nil {
		return nil, fmt.Errorf("认证文件解密失败（密钥文件换了？）：%w", err)
	}
	return plain, nil
}

// LoadSealedJSON 读一个加密 JSON 文件。文件不存在时返回 (false, nil)。
func LoadSealedJSON(path, keyPath string, v any) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return false, err
	}
	plain, err := openBlob(key, raw)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(plain, v); err != nil {
		return false, fmt.Errorf("认证文件 %s 解析失败：%w", path, err)
	}
	return true, nil
}

// SaveSealedJSON 加密写入（0600，先写 .tmp 再 rename，避免写一半断电留下坏文件）。
func SaveSealedJSON(path, keyPath string, v any) error {
	plain, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return err
	}
	sealed, err := sealBlob(key, plain)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
