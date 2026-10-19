// 本地保存的**登录令牌**。
//
// 跟 client-id 一样放在数据目录里（见 internal/appdir —— 用户可以把它改到 D 盘之类），
// 但它比 client-id 要紧得多：client-id 只是个"认人用的号码"，
// 而令牌是**能直接连服务器的凭据** ⇒ 文件权限 0600，只给当前用户读 ✓
//
// 📌 为什么单独存一个文件、而不是塞进 settings.json：
// settings.json 是给用户自己改的（还有"恢复默认设置"这种操作会把整个文件重写），
// 凭据跟它混在一起，迟早被哪次重置顺手抹掉或者泄进备份里 ✗
package identity

import (
	"os"
	"path/filepath"
	"strings"

	"p2plink/internal/appdir"
)

const tokenFile = "token"

func tokenPath() string {
	dir := appdir.Dir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, tokenFile)
}

// Token 读登录令牌；没有（或读不出来）就返回空串 = 没登录。
//
// 🩸 读失败一律当"没登录"，**不报错**：调用方唯一的正确反应就是让用户去登录，
// 而"文件读不出来"这件事对用户来说没有任何可操作的信息 ✓
func Token() string {
	p := tokenPath()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SetToken 存令牌（登录成功后调）。
func SetToken(tok string) error {
	p := tokenPath()
	if p == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// 0600：这是凭据，同机别的用户不该读到
	return os.WriteFile(p, []byte(strings.TrimSpace(tok)), 0o600)
}

// ClearToken 抹掉令牌（退出登录 / 服务器说"请重新登录"时调）。
func ClearToken() error {
	p := tokenPath()
	if p == "" {
		return nil
	}
	err := os.Remove(p)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
