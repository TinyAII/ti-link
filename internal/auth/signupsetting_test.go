// 注册开关（signupOpen）的测试。
//
// 🩸 最要紧的一条是 TestSignupOpenDefaultsToClosed：
//
//	**老库文件里没有 settings 字段** ⇒ 读出来必须仍然是"要邀请码" ✓
//	这条要是做反了（比如用 NeedsCode 语义），一升级就门户大开，而且
//	平时完全看不出来（注册成功了嘛）✗
package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(dir, true)
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	return st, dir
}

// ⭐⭐ 新库 / 老库（没有 settings 字段）⇒ 都必须是"要邀请码"
//
// 反证方法：把 Settings 的默认值改成 true（或把字段语义反过来）⇒ 立刻红 ✓
func TestSignupOpenDefaultsToClosed(t *testing.T) {
	st, _ := openTestStore(t)
	if st.SignupOpen() {
		t.Fatal("新库默认就该是「要邀请码」，实得开放 ✗")
	}

	// 造一个**没有 settings 字段**的老文件（模拟升级前写下的库）
	dir := t.TempDir()
	old := `{"version":1,"accounts":[],"codes":[]}`
	if err := writeOldAccountsFile(dir, old); err != nil {
		t.Fatalf("造老库失败: %v", err)
	}
	st2, err := Open(dir, true)
	if err != nil {
		t.Fatalf("读老库失败: %v", err)
	}
	if st2.SignupOpen() {
		t.Fatal("老库（没有 settings 字段）必须仍然「要邀请码」 ✗ 一升级就门户大开是最坏的情况")
	}
}

// ⭐ 没开放 ⇒ 没码注册不了（现状行为，不能被这次改动弄坏）
func TestSignUpStillNeedsCodeWhenClosed(t *testing.T) {
	st, _ := openTestStore(t)
	now := time.Now()

	if _, _, err := st.SignUp("", "a@test.local", "pass-1234", []string{"test.local"}, now); err != ErrBadCode {
		t.Fatalf("没开放时没码该报 ErrBadCode，实得 %v ✗", err)
	}
	if _, _, err := st.SignUp("瞎编的码", "b@test.local", "pass-1234", []string{"test.local"}, now); err != ErrBadCode {
		t.Fatalf("没开放时错码该报 ErrBadCode，实得 %v ✗", err)
	}
}

// ⭐⭐ 开放 ⇒ 没码也能注册；而且注册出来的账号 Code 是空的（不消耗任何码）
//
// 反证方法：把 SignUp 里那两处 `if !open || nc != ""` 改回原来那句 ⇒ 立刻红 ✓
func TestSignUpOpenWithoutCode(t *testing.T) {
	st, _ := openTestStore(t)
	now := time.Now()
	if err := st.SetSignupOpen(true); err != nil {
		t.Fatalf("打开开放注册失败: %v", err)
	}

	a, tok, err := st.SignUp("", "c@test.local", "pass-1234", []string{"test.local"}, now)
	if err != nil {
		t.Fatalf("开放注册时没填码该能注册，实得 %v ✗", err)
	}
	if a == nil || tok == "" {
		t.Fatal("该拿到账号和令牌 ✗")
	}
	if a.Code != "" {
		t.Errorf("没填码注册的账号，Code 该是空串，实得 %q ✗（难道消耗了某个码？）", a.Code)
	}
}

// ⭐ 开放 + **填了错的码** ⇒ 仍然报错（别静默放过：用户会以为码用上了，实际没有）
func TestSignUpOpenButWrongCodeStillFails(t *testing.T) {
	st, _ := openTestStore(t)
	now := time.Now()
	if err := st.SetSignupOpen(true); err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	if _, _, err := st.SignUp("瞎编的码", "d@test.local", "pass-1234", []string{"test.local"}, now); err != ErrBadCode {
		t.Fatalf("填了错码该报 ErrBadCode，实得 %v ✗", err)
	}
}

// ⭐ 开放 + 填了**对的码** ⇒ 注册成功，而且码确实被标记已用
func TestSignUpOpenWithValidCodeConsumesIt(t *testing.T) {
	st, _ := openTestStore(t)
	now := time.Now()
	if err := st.SetSignupOpen(true); err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	codes, err := st.GenCodes(1, "t", "", now)
	if err != nil {
		t.Fatalf("生成邀请码失败: %v", err)
	}
	a, _, err := st.SignUp(codes[0], "e@test.local", "pass-1234", []string{"test.local"}, now)
	if err != nil {
		t.Fatalf("用有效码注册该成功，实得 %v ✗", err)
	}
	if a.Code != NormalizeCode(codes[0]) {
		t.Errorf("账号该记下用了哪个码，实得 %q ✗", a.Code)
	}
	// 同一个码不能再用第二次
	if _, _, err := st.SignUp(codes[0], "f@test.local", "pass-1234", []string{"test.local"}, now); err != ErrBadCode {
		t.Fatalf("用过的码该报 ErrBadCode，实得 %v ✗", err)
	}
}

// ⭐ 开关要**落盘**：改完重开一次库，还是那个值
//
// 反证方法：把 SetSignupOpen 里的 saveAccountsLocked() 删掉 ⇒ 立刻红 ✓
func TestSignupOpenPersists(t *testing.T) {
	st, dir := openTestStore(t)
	if err := st.SetSignupOpen(true); err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	st2, err := Open(dir, true)
	if err != nil {
		t.Fatalf("重开库失败: %v", err)
	}
	if !st2.SignupOpen() {
		t.Fatal("改了开关却没落盘 ✗（重开库之后又变回「要邀请码」了）")
	}
}

// writeOldAccountsFile 往目录里写一份"升级前形状"的 accounts.json（没有 settings 字段）。
//
// 📌 直接写文件而不是用 Store —— 要的就是"文件里没有那个字段"这个状态 ✓
func writeOldAccountsFile(dir, body string) error {
	return os.WriteFile(filepath.Join(dir, "accounts.json"), []byte(body), 0o600)
}
