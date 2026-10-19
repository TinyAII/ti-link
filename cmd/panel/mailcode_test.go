// 邮箱验证码那一层的测试。
//
// 🩸 这里最要紧的两类断言：
//
//	① **码本身的性质**：一次性、会过期、错多了作废（少哪条都是个洞）
//	② **限流**：不设限的话，这个接口就是一台拿着我们的邮箱对外群发的机器 ✗
package main

import (
	"strings"
	"testing"
	"time"
)

var codeT0 = time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)

func newCodes() *mailCodeStore { return newMailCodeStore([]byte("test-key-0123456789abcdef")) }

func mustSend(t *testing.T, s *mailCodeStore, email, ip string, at time.Time) string {
	t.Helper()
	code, why := s.beginSend(email, ip, at)
	if why != "" {
		t.Fatalf("要验证码失败: %s", why)
	}
	return code
}

func TestCodeShape(t *testing.T) {
	s := newCodes()
	for i := 0; i < 50; i++ {
		c := mustSend(t, s, "a@qq.com", "1.2.3.4", codeT0.Add(time.Duration(i)*time.Hour))
		if len(c) != codeLen {
			t.Fatalf("码该是 %d 位，实得 %q", codeLen, c)
		}
		for _, r := range c {
			if r < '0' || r > '9' {
				t.Fatalf("码该全是数字，实得 %q", c)
			}
		}
	}
}

// ⭐ 一次性：用过的码立刻作废（不然一个码能登两次）
func TestCodeSingleUse(t *testing.T) {
	s := newCodes()
	c := mustSend(t, s, "a@qq.com", "1.2.3.4", codeT0)
	if ok, _ := s.verify("a@qq.com", c, codeT0); !ok {
		t.Fatal("第一次该过")
	}
	if ok, _ := s.verify("a@qq.com", c, codeT0); ok {
		t.Fatal("同一个码第二次该被拒 ✗")
	}
}

// 码只认它自己那个邮箱 —— 换个邮箱拿同一个码不该过
func TestCodeBoundToEmail(t *testing.T) {
	s := newCodes()
	c := mustSend(t, s, "a@qq.com", "1.2.3.4", codeT0)
	if ok, _ := s.verify("b@qq.com", c, codeT0); ok {
		t.Fatal("码不该能跨邮箱用 ✗")
	}
	// 而且"试错"要算在 a@qq.com 头上（不然拿它当探测器能把码试出来）
	if ok, _ := s.verify("a@qq.com", c, codeT0); !ok {
		t.Fatal("发对了的那个邮箱该还能用")
	}
}

func TestCodeExpires(t *testing.T) {
	s := newCodes()
	c := mustSend(t, s, "a@qq.com", "1.2.3.4", codeT0)
	if ok, _ := s.verify("a@qq.com", c, codeT0.Add(codeTTL-time.Second)); !ok {
		t.Fatal("还没到期该能用")
	}
	c2 := mustSend(t, s, "a@qq.com", "1.2.3.4", codeT0.Add(time.Hour))
	if ok, why := s.verify("a@qq.com", c2, codeT0.Add(time.Hour+codeTTL+time.Second)); ok {
		t.Fatal("过期了该被拒 ✗")
	} else if !strings.Contains(why, "过期") {
		t.Errorf("该说「过期」，实得 %q", why)
	}
}

// ⭐ 错太多次就作废 —— 不然 6 位数字是可以被穷举的
func TestCodeTooManyTries(t *testing.T) {
	s := newCodes()
	c := mustSend(t, s, "a@qq.com", "1.2.3.4", codeT0)
	wrong := "000000"
	if c == wrong {
		wrong = "111111"
	}
	for i := 0; i < codeMaxTries; i++ {
		if ok, _ := s.verify("a@qq.com", wrong, codeT0); ok {
			t.Fatal("错的码不该过")
		}
	}
	// 试满了之后，**连正确的码也不认了**（作废）
	if ok, why := s.verify("a@qq.com", c, codeT0); ok {
		t.Fatalf("试错超过 %d 次该整个作废，实得通过 ✗", codeMaxTries)
	} else if !strings.Contains(why, "次数") {
		t.Errorf("该说「试错次数」，实得 %q", why)
	}
}

// 空码：给一句人话，别走上哈希那条路
func TestEmptyCode(t *testing.T) {
	s := newCodes()
	if ok, why := s.verify("a@qq.com", "", codeT0); ok || why == "" {
		t.Fatalf("空码该被拒且给个理由，实得 ok=%v why=%q", ok, why)
	}
}

// ⭐ 60 秒重发间隔
func TestCodeResendGap(t *testing.T) {
	s := newCodes()
	mustSend(t, s, "a@qq.com", "1.2.3.4", codeT0)
	if _, why := s.beginSend("a@qq.com", "1.2.3.4", codeT0.Add(10*time.Second)); why == "" {
		t.Fatal("10 秒内重发该被挡 ✗")
	} else if !strings.Contains(why, "秒") {
		t.Errorf("该告诉他还差几秒，实得 %q", why)
	}
	if _, why := s.beginSend("a@qq.com", "1.2.3.4", codeT0.Add(codeResendGap+time.Second)); why != "" {
		t.Fatalf("过了间隔该能再发，实得 %q", why)
	}
}

// ⭐ 每邮箱每小时上限（防止盯着一个邮箱猛发）
func TestCodePerEmailLimit(t *testing.T) {
	s := newCodes()
	got := 0
	for i := 0; i < codePerEmail+3; i++ {
		// 每次隔过重发间隔，这样才能试出"每小时"那道闸
		at := codeT0.Add(time.Duration(i) * (codeResendGap + time.Second))
		if _, why := s.beginSend("a@qq.com", "1.2.3.4", at); why == "" {
			got++
		} else if !strings.Contains(why, "频繁") {
			t.Errorf("第 %d 次被挡该说「频繁」，实得 %q", i, why)
		}
	}
	if got > codePerEmail {
		t.Fatalf("每小时该最多 %d 封，实得 %d ✗", codePerEmail, got)
	}
}

// ⭐ 每 IP 每小时上限（防止换着邮箱刷 —— 那才是真的被人当发信机用）
func TestCodePerIPLimit(t *testing.T) {
	s := newCodes()
	got := 0
	for i := 0; i < codePerIP+5; i++ {
		email := string(rune('a'+i%26)) + string(rune('0'+i/26)) + "@qq.com"
		at := codeT0.Add(time.Duration(i) * (codeResendGap + time.Second))
		if _, why := s.beginSend(email, "9.9.9.9", at); why == "" {
			got++
		}
	}
	if got > codePerIP {
		t.Fatalf("每个 IP 每小时该最多 %d 封，实得 %d ✗", codePerIP, got)
	}
}

// 发信失败之后要把刚存的码吊销（不然用户白等 60 秒）
func TestRevokeFreesResendGap(t *testing.T) {
	s := newCodes()
	mustSend(t, s, "a@qq.com", "1.2.3.4", codeT0)
	s.revoke("a@qq.com")
	if _, why := s.beginSend("a@qq.com", "1.2.3.4", codeT0.Add(time.Second)); why != "" {
		t.Fatalf("吊销之后该能立刻重发，实得 %q ✗", why)
	}
}

// ⭐⭐ 存进去的**不能是明文**。
//
// 反证方法：把 beginSend 里的 s.hash(...) 换成直接存 code，这条立刻红 ✓
// 为什么值得钉：内存里那份要是明文（将来加个内存转储/调试接口就会漏），
// 而"读到码"等于"能登进那个账号" ✗
func TestCodeStoredHashed(t *testing.T) {
	s := newCodes()
	c := mustSend(t, s, "a@qq.com", "1.2.3.4", codeT0)
	s.mu.Lock()
	e := s.m["a@qq.com"]
	s.mu.Unlock()
	if e == nil {
		t.Fatal("该有一条记录")
	}
	if string(e.hash) == c {
		t.Fatal("存的是明文码 —— 该存 HMAC ✗")
	}
	if strings.Contains(string(e.hash), c) {
		t.Fatal("哈希里能翻出明文码 ✗")
	}
}

// 清理：过期的记录和闲置的限流桶都要扔掉（不然是慢性内存泄漏）
//
// 🩸 断言要**指名道姓**说清哪一条被清了 —— 只比数量的话，
// 把判据反过来（该清的留、该留的清）数量照样对得上 ✗（这坑踩过）
func TestCodeCleanup(t *testing.T) {
	s := newCodes()
	mustSend(t, s, "old@qq.com", "1.2.3.4", codeT0)                                // 会过期
	mustSend(t, s, "new@qq.com", "5.6.7.8", codeT0.Add(codeResendGap+time.Second)) // 还早

	// 清理时刻：第一个已经过期（T0+5min），第二个才发了 4 分钟
	n := s.cleanup(codeT0.Add(codeTTL + time.Second))
	if n < 1 {
		t.Fatalf("过期的码该被清掉，实得 %d 条", n)
	}
	if s.has("old@qq.com") {
		t.Error("过期的那条该被清掉 ✗")
	}
	if !s.has("new@qq.com") {
		t.Error("还没到期的那条**不该**被清掉 ✗")
	}
}

func (s *mailCodeStore) has(email string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[email]
	return ok
}
