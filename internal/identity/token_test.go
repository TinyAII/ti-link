// 登录令牌的存取。
//
// 🩸 它比 client-id 要紧得多：client-id 只是个"认人用的号码"，
// 而令牌是**能直接连服务器的凭据** ⇒ 文件权限、失败时的行为都得钉住 ✓
package identity

import (
	"os"
	"path/filepath"
	"testing"

	"p2plink/internal/appdir"
)

// 这个测试会往数据目录里写文件 ⇒ 先顶掉数据目录（见 identity_test.go 的注释）。
func setupDir(t *testing.T) {
	t.Helper()
	if appdir.IsCustom() {
		t.Skip("数据目录被用户改到别处了 —— 这测试会往那儿写文件，跳过")
	}
	t.Setenv(appdir.EnvDir, t.TempDir())
}

func TestTokenRoundTrip(t *testing.T) {
	setupDir(t)

	// 一开始没有 ⇒ 空串（= 没登录），不是报错
	if got := Token(); got != "" {
		t.Fatalf("还没登录时该是空串，实得 %q", got)
	}

	if err := SetToken("abc-123"); err != nil {
		t.Fatal(err)
	}
	if got := Token(); got != "abc-123" {
		t.Fatalf("存进去又读出来对不上：%q", got)
	}

	if err := ClearToken(); err != nil {
		t.Fatal(err)
	}
	if got := Token(); got != "" {
		t.Fatalf("清掉之后该是空串，实得 %q", got)
	}
	// 再清一次不该报错（幂等）—— 退出登录点两下不该炸
	if err := ClearToken(); err != nil {
		t.Fatalf("重复清该安静通过，实得 %v", err)
	}
}

// 🩸 令牌是凭据，文件权限必须是 0600（只有当前用户能读）。
// 同机别的用户读到它 = 能直接拿你的号连服务器 ✗
func TestTokenFileIsPrivate(t *testing.T) {
	setupDir(t)
	if err := SetToken("secret-token"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(appdir.Dir(), "token")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// Windows 上 os 报的权限位是"看起来像 0666"那种，不看它 ——
	// 但**内容必须只是令牌本身**（不带换行、不带别的），这条在两边都成立 ✓
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "secret-token" {
		t.Fatalf("文件内容该正好是令牌本身，实得 %q", string(b))
	}
	if fi.Size() == 0 {
		t.Fatal("文件是空的")
	}
}

// 前后空格要收掉 —— 用户从别处复制令牌常常会带上空格/换行，
// 带着它去连服务器就是"令牌不认"，而错误信息完全看不出来是空格的问题 ✗
func TestTokenTrimmed(t *testing.T) {
	setupDir(t)
	if err := SetToken("  tok\n"); err != nil {
		t.Fatal(err)
	}
	if got := Token(); got != "tok" {
		t.Fatalf("存的时候该去掉空白，实得 %q", got)
	}
}

// 令牌文件被外部改坏（或者数据目录整个不见了）⇒ 返回空串、**不报错**。
// 调用方唯一的正确反应就是"让他去登录"，报错没有任何可操作的信息 ✓
func TestTokenSurvivesBrokenState(t *testing.T) {
	setupDir(t)
	p := filepath.Join(appdir.Dir(), "token")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// 造一个**目录**顶着 token 这个名字 —— 读它必然失败
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Token(); got != "" {
		t.Fatalf("读不出来该回空串（= 没登录），实得 %q", got)
	}
}
