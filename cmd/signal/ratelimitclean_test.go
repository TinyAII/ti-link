package main

import (
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// IP 限流表的清理逻辑。
//
// 🩸 核心那条：**判据是"闲置够久"，不是"没有活跃连接"** ✗
//
// 两者差别很大 —— 只看 `active == 0` 的话，条目被删 = 限流器状态一起没了
// ⇒ 攻击者只要"打满 → 等一个清理周期 → 额度又满了"，
// `connsPerSecond` 这个限制就白设了 ✓
//
// 📌 这里**调的是生产代码里的 `canDropIP`**，不是把判据抄一遍 ✓
// 抄一遍的话，测的是抄本 —— 生产改坏了这儿也不红 ✗
// （2026-10-15 第一版就是这么写错的，反证时才发现 ✓）
func TestCanDropIP(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name     string
		active   int
		lastSeen time.Time
		want     bool
	}{
		// ① 正在用的 —— **绝不能清** ✗（哪怕闲置了很久）
		{"正在用（闲置 10 小时）", 3, now.Add(-10 * time.Hour), false},
		{"正在用（刚来）", 1, now, false},

		// ② 没在用、但**刚来过** ⇒ 不能清 ✓✓
		// 🩸 这一组就是防"等清理重置额度"的 —— 判据一旦退化成
		// `active == 0`，这几条立刻红 ✓
		{"没在用，但 5 秒前才来过", 0, now.Add(-5 * time.Second), false},
		{"没在用，闲置半小时", 0, now.Add(-30 * time.Minute), false},
		{"没在用，刚好没到 TTL", 0, now.Add(-idleIPLimitTTL + time.Minute), false},

		// ③ 真的闲置够久了 ⇒ 才清 ✓
		{"闲置超过 TTL", 0, now.Add(-2 * time.Hour), true},
		{"闲置刚好过 TTL 一点", 0, now.Add(-idleIPLimitTTL - time.Second), true},
	}

	for _, c := range cases {
		st := &ipLimit{lim: newTestLimiter(), active: c.active, lastSeen: c.lastSeen}
		if got := canDropIP(st, now); got != c.want {
			t.Errorf("%s：canDropIP 该是 %v，实得 %v ✗", c.name, c.want, got)
		}
	}
}

// 走一遍真正的清理流程：闲置够久的被清、刚来过的留下 ✓
func TestCleanSweepBehavior(t *testing.T) {
	const stale = "198.51.100.77"
	const fresh = "198.51.100.88"

	ipMu.Lock()
	ipLimits[stale] = &ipLimit{lim: newTestLimiter(), lastSeen: time.Now().Add(-3 * time.Hour)}
	ipLimits[fresh] = &ipLimit{lim: newTestLimiter(), lastSeen: time.Now()}
	ipMu.Unlock()

	// 跟 cleanIPLimits 里**同一个循环体**（只是不 sleep）✓
	now := time.Now()
	ipMu.Lock()
	for ip, st := range ipLimits {
		if canDropIP(st, now) {
			delete(ipLimits, ip)
		}
	}
	ipMu.Unlock()

	ipMu.Lock()
	_, staleThere := ipLimits[stale]
	_, freshThere := ipLimits[fresh]
	ipMu.Unlock()

	if staleThere {
		t.Error("闲置 3 小时的条目该被清掉 ✗")
	}
	if !freshThere {
		t.Error("刚来过的条目**不该**被清 ✗（清了就等于重置它的限流额度）")
	}

	ipMu.Lock()
	delete(ipLimits, fresh)
	ipMu.Unlock()
}

// releaseConn 也要刷新 lastSeen —— 不然"最后一个连接断开"那一刻起时间戳就冻住，
// 一个断断续续在用的 IP 会被误判成"闲置很久"，然后被清掉（额度又重置了）✗
func TestReleaseUpdatesLastSeen(t *testing.T) {
	const ip = "203.0.113.99"

	ipMu.Lock()
	ipLimits[ip] = &ipLimit{
		lim:      newTestLimiter(),
		active:   1,
		lastSeen: time.Now().Add(-2 * time.Hour),
	}
	ipMu.Unlock()

	releaseConn(ip)

	ipMu.Lock()
	st := ipLimits[ip]
	ipMu.Unlock()
	if st == nil {
		t.Fatal("releaseConn 不该把条目删掉")
	}
	if time.Since(st.lastSeen) > time.Minute {
		t.Error("releaseConn 该刷新 lastSeen ✗（否则断断续续在用的 IP 会被误判成闲置）")
	}
	if st.active != 0 {
		t.Errorf("active 该归零，实得 %d ✗", st.active)
	}
	if canDropIP(st, time.Now()) {
		t.Error("刚 release 完的条目**不该**被判成可清 ✗")
	}

	ipMu.Lock()
	delete(ipLimits, ip)
	ipMu.Unlock()
}

// newTestLimiter 造一个跟生产同参数的限流器 ✓
func newTestLimiter() *rate.Limiter {
	return rate.NewLimiter(rate.Limit(connsPerSecond), connsPerSecond)
}
