// Package identity 管理客户端本地持久 ID。
// 它存在数据目录里（见 internal/appdir —— 那个目录用户可以改到 D 盘之类），
// 用来让服务器识别「是不是同一个人」（拉黑功能依赖它）。
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"p2plink/internal/appdir"
)

// ClientID 读取本地 ID；没有就生成一个并存下来。
// 出错时退化成临时 ID（本次会话有效，不持久化）。
func ClientID() string {
	dir := appdir.Dir()
	if dir == "" {
		return randomID()
	}
	path := filepath.Join(dir, "client-id")
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	id := randomID()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		_ = os.WriteFile(path, []byte(id), 0o644)
	}
	return id
}

func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}
