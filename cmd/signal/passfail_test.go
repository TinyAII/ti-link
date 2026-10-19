// 房间密码失败退避的测试（M4）。
//
// 📌 全部用注入的 `now` 跳时间 —— 不真等 30 秒 ✓
//
// 🩸 这几个函数**不加锁**（调用方必须持锁，见 passfail.go 顶部）⇒
//
//	测试里调之前自己 Lock 一下 ✓ 这也顺带验证了"加锁调用是安全的" ✓
package main

import (
	"fmt"
	"testing"
	"time"
)

func passFailRoom() *room { return &room{code: "T-PF0001", bans: map[string]*ban{}} }

// 持锁调一下（跟 join.go 里的用法一致）✓
func blocked(r *room, ip string, now time.Time) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return passBlockedLocked(r, ip, now)
}

func fail(r *room, ip string, now time.Time) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return notePassFailLocked(r, ip, now)
}

func ok(r *room, ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	clearPassFailLocked(r, ip)
}

// ⭐ 错 1、2 次只记账**不锁**（正常人也会手滑）✓
//
// 反证方法：把 passFailFree 改成 1 ⇒ 立刻红 ✓
func TestPassFailFreeTries(t *testing.T) {
	r := passFailRoom()
	now := time.Now()

	for i := 1; i <= 2; i++ {
		if lock := fail(r, "1.1.1.1", now); lock != 0 {
			t.Fatalf("第 %d 次错不该锁，却锁了 %v ✗", i, lock)
		}
		if d := blocked(r, "1.1.1.1", now); d != 0 {
			t.Fatalf("第 %d 次错之后不该被挡，实得还要等 %v ✗", i, d)
		}
	}
}

// ⭐⭐ **错 3 次 ⇒ 锁 30 秒**（老板拍的值），锁定期间进不来
//
// 反证方法：把 `f.n < passFailFree` 改成 `< 999` ⇒ 立刻红 ✓
func TestPassFailLocksOnThird(t *testing.T) {
	r := passFailRoom()
	now := time.Now()

	fail(r, "1.1.1.1", now)
	fail(r, "1.1.1.1", now)
	if lock := fail(r, "1.1.1.1", now); lock != passFailBase {
		t.Fatalf("错 3 次该锁 %v，实得 %v ✗", passFailBase, lock)
	}

	// 锁着的时候要被挡 ✓
	if d := blocked(r, "1.1.1.1", now); d <= 0 {
		t.Fatal("锁上了却没被挡 ✗")
	}
	// 29 秒后还在锁 ✓
	if d := blocked(r, "1.1.1.1", now.Add(29*time.Second)); d <= 0 {
		t.Fatal("29 秒就放行了？该锁满 30 秒 ✗")
	}
	// 31 秒后放行 ✓
	if d := blocked(r, "1.1.1.1", now.Add(31*time.Second)); d != 0 {
		t.Fatalf("30 秒过了该放行，实得还要等 %v ✗", d)
	}
}

// ⭐ 之后每次错都**翻倍**，但不超过 10 分钟 ✓
func TestPassFailDoublesAndCaps(t *testing.T) {
	r := passFailRoom()
	now := time.Now()

	want := []time.Duration{
		0, 0, // 第 1、2 次：不锁
		passFailBase,      // 3 ⇒ 30s
		passFailBase * 2,  // 4 ⇒ 60s
		passFailBase * 4,  // 5 ⇒ 120s
		passFailBase * 8,  // 6 ⇒ 240s
		passFailBase * 16, // 7 ⇒ 480s
		passFailMax,       // 8 ⇒ 封顶 600s
		passFailMax,       // 9 ⇒ 还是 600s
	}
	for i, w := range want {
		if got := fail(r, "1.1.1.1", now); got != w {
			t.Fatalf("第 %d 次错该锁 %v，实得 %v ✗", i+1, w, got)
		}
	}
}

// ⭐ 中间对了一次 ⇒ **整笔清零**（不然手滑过的人下次一错就被锁，体验很糟）✗
//
// 反证方法：把 clearPassFailLocked 的函数体改成空 ⇒ 立刻红 ✓
func TestPassFailClearedOnSuccess(t *testing.T) {
	r := passFailRoom()
	now := time.Now()

	fail(r, "1.1.1.1", now)
	fail(r, "1.1.1.1", now)
	ok(r, "1.1.1.1") // 这一步"输对了"

	// 清零之后再错两次都不该锁 ✓
	if lock := fail(r, "1.1.1.1", now); lock != 0 {
		t.Fatalf("清零后第一次错就锁了 %v ✗", lock)
	}
	if lock := fail(r, "1.1.1.1", now); lock != 0 {
		t.Fatalf("清零后第二次错就锁了 %v ✗", lock)
	}
}

// ⭐ 不同来源互不影响（一个人在 A 房试错，不该牵连别人）✓
func TestPassFailPerIP(t *testing.T) {
	r := passFailRoom()
	now := time.Now()

	fail(r, "1.1.1.1", now)
	fail(r, "1.1.1.1", now)
	fail(r, "1.1.1.1", now) // 这个 IP 被锁了

	if d := blocked(r, "2.2.2.2", now); d != 0 {
		t.Fatalf("另一个来源被连累了？实得还要等 %v ✗", d)
	}
	if lock := fail(r, "2.2.2.2", now); lock != 0 {
		t.Fatalf("另一个来源第一次错就锁了 %v ✗", lock)
	}
}

// ⭐ 条目上限：拿一大堆来源刷也**不许把内存刷爆**（超了就整表清空，但功能还正常）✓
//
// 反证方法：把 passFailMaxEntries 那段删掉 ⇒ 这条会留下 500 条（不算"红"但能看出没清）；
//
//	真正会红的是把整表清空那段写错导致 panic ✓
func TestPassFailEntryCap(t *testing.T) {
	r := passFailRoom()
	now := time.Now()

	for i := 0; i < passFailMaxEntries*2; i++ {
		ip := fmt.Sprintf("10.0.%d.%d", i/256, i%256)
		fail(r, ip, now)
	}

	r.mu.Lock()
	n := len(r.passFails)
	r.mu.Unlock()
	if n > passFailMaxEntries {
		t.Fatalf("条目数该被压在上限内，实得 %d ✗（这就是内存被刷爆的路径）", n)
	}
	// 清空之后功能还得正常：新的来源还是从 1 开始数 ✓
	if lock := fail(r, "9.9.9.9", now); lock != 0 {
		t.Fatalf("清空之后第一次错就锁了 %v ✗", lock)
	}
}
