// 找回密码（ResetPassword）的测试 —— 上一条欠的，这里补上。
//
// 🩸 最要紧的两条：
//
//	· 改完**旧令牌必须失效**（不然密码被人改过之后，偷窥者的会话还在）
//	· 改完**旧密码登不进、新密码能登**
//
// ⚠️ 这个函数是唯一不需要旧密码就能改密码的入口 ⇒ 它出问题就是账号被人拿走 ✗
package auth

import (
	"testing"
	"time"
)

// 造一个可用的账号（用真邀请码走一趟 SignUp，避免直接拼结构体）
func mkAccount(t *testing.T, st *Store, email, pass string, now time.Time) (*Account, string) {
	t.Helper()
	codes, err := st.GenCodes(1, "t", "", now)
	if err != nil {
		t.Fatalf("生成邀请码失败: %v", err)
	}
	a, tok, err := st.SignUp(codes[0], email, pass, []string{"test.local"}, now)
	if err != nil {
		t.Fatalf("建账号失败: %v", err)
	}
	return a, tok
}

// ⭐⭐ 核心：改完密码 ⇒ 旧令牌失效、旧密码登不进、新密码能登
//
// 反证方法：把 ResetPassword 结尾的 `return s.revokeLocked(id, now)` 改成 `return nil`
//
//	⇒ "旧令牌失效"那条立刻红 ✓
func TestResetPasswordChangesAndRevokes(t *testing.T) {
	st, _ := openTestStore(t)
	now := time.Now()
	const email = "r@test.local"
	a, oldTok := mkAccount(t, st, email, "old-pass-1234", now)

	// 改之前先确认：旧令牌是有效的 ✓（否则下面的断言等于没测）
	if _, ok := st.VerifyToken(oldTok, now); !ok {
		t.Fatal("前提不成立：新账号的令牌本来就无效")
	}

	if err := st.ResetPassword(email, "new-pass-5678", now); err != nil {
		t.Fatalf("重置密码失败: %v", err)
	}

	// ① 旧令牌必须失效
	if _, ok := st.VerifyToken(oldTok, now); ok {
		t.Error("改完密码，**旧令牌还是有效的** ✗ 偷窥者的会话没被踢掉")
	}
	// ② 旧密码登不进
	if _, _, err := st.Login(email, "old-pass-1234", now); err == nil {
		t.Error("旧密码还能登录 ✗ 密码根本没改成功")
	}
	// ③ 新密码能登
	if _, tok, err := st.Login(email, "new-pass-5678", now); err != nil {
		t.Errorf("新密码登不进 ✗（%v）", err)
	} else if tok == "" {
		t.Error("登录成功却没给令牌 ✗")
	}
	// ④ 账号 id 没变（别一不小心重建了个账号）
	if a2, ok := st.FindByEmail(email); !ok || a2.ID != a.ID {
		t.Error("重置密码不该换账号 ID ✗")
	}
}

// ⭐ 不存在的邮箱 ⇒ ErrNotFound（调用方按这个回"还没注册"）
func TestResetPasswordUnknownEmail(t *testing.T) {
	st, _ := openTestStore(t)
	if err := st.ResetPassword("nobody@test.local", "new-pass-5678", time.Now()); err != ErrNotFound {
		t.Fatalf("不存在的邮箱该返回 ErrNotFound，实得 %v ✗", err)
	}
}

// ⭐ 新密码不合规 ⇒ 报错，而且**老密码不能被搞坏**（这点容易漏）
func TestResetPasswordRejectsWeakPassword(t *testing.T) {
	st, _ := openTestStore(t)
	now := time.Now()
	const email = "w@test.local"
	mkAccount(t, st, email, "old-pass-1234", now)

	if err := st.ResetPassword(email, "123", now); err == nil {
		t.Fatal("弱密码该被拒 ✗")
	}
	// 老密码还得能登（失败的重置不该动到任何东西）
	if _, _, err := st.Login(email, "old-pass-1234", now); err != nil {
		t.Errorf("重置失败却把老密码弄坏了 ✗（%v）", err)
	}
}

// ⭐ 改完要**落盘**：重开一次库，新密码依然有效
//
// 反证方法：把 ResetPassword 里的 revokeLocked 换成一个不落盘的函数 ⇒ 立刻红
//
//	（revokeLocked 内部会 save ✓）
func TestResetPasswordPersists(t *testing.T) {
	st, dir := openTestStore(t)
	now := time.Now()
	const email = "p@test.local"
	mkAccount(t, st, email, "old-pass-1234", now)

	if err := st.ResetPassword(email, "new-pass-5678", now); err != nil {
		t.Fatalf("重置失败: %v", err)
	}

	st2, err := Open(dir, true)
	if err != nil {
		t.Fatalf("重开库失败: %v", err)
	}
	if _, _, err := st2.Login(email, "new-pass-5678", now); err != nil {
		t.Errorf("重开库后新密码失效 ✗（没落盘）: %v", err)
	}
}
