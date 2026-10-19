package main

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 15, 22, 0, 0, 0, time.UTC)

func TestLimiterBlocksAfterLimit(t *testing.T) {
	l := newLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("ip:1.2.3.4", t0); !ok {
			t.Fatalf("第 %d 次就被挡了，限额是 3", i+1)
		}
	}
	ok, retry := l.allow("ip:1.2.3.4", t0)
	if ok {
		t.Fatal("第 4 次该被挡下来 ✗")
	}
	if retry <= 0 || retry > time.Minute {
		t.Errorf("建议等待时间不该是 %v", retry)
	}
}

// 窗口过了就该重新放行 —— 否则误伤的人永远出不来。
func TestLimiterWindowResets(t *testing.T) {
	l := newLimiter(2, time.Minute)
	l.allow("k", t0)
	l.allow("k", t0)
	if ok, _ := l.allow("k", t0); ok {
		t.Fatal("窗口内第 3 次该被挡")
	}
	if ok, _ := l.allow("k", t0.Add(time.Minute)); !ok {
		t.Fatal("过了窗口该重新放行 ✗")
	}
}

// 不同键之间互不影响（一个宿舍的出口 IP 被限了，不该连累别人）。
func TestLimiterKeysAreIndependent(t *testing.T) {
	l := newLimiter(1, time.Minute)
	if ok, _ := l.allow("a", t0); !ok {
		t.Fatal("a 第一次该过")
	}
	if ok, _ := l.allow("b", t0); !ok {
		t.Fatal("b 第一次该过 —— 不同的键不该互相影响 ✗")
	}
}

func TestLimiterReset(t *testing.T) {
	l := newLimiter(1, time.Minute)
	l.allow("k", t0)
	l.reset("k")
	if ok, _ := l.allow("k", t0); !ok {
		t.Fatal("reset 之后该重新有额度 ✗")
	}
}

// ⭐ 清理：不清理的话"每个来过的 IP 都留一条"就是慢性内存泄漏。
//
// 🩸 这里**必须验"清掉的是哪一条"**，不能只验数量 —— 只验数量的话，
// 把判据反过来写（该留的清了、该清的留了）数量照样是 1，测试照样绿 ✗
// （2026-10-15 反证时就是这么发现的。）
// 反证方法：把 canDropBucket 的 `>` 改成 `<`，这条立刻红 ✓
func TestLimiterCleanup(t *testing.T) {
	l := newLimiter(5, time.Minute)
	l.allow("old", t0)
	l.allow("new", t0.Add(10*time.Minute))

	// idle 定 5 分钟 ⇒ t0+11min 时："old" 闲了 11 分钟（该清）、
	// "new" 才闲 1 分钟（该留）
	n := l.cleanup(t0.Add(11*time.Minute), 5*time.Minute)
	if n != 1 {
		t.Fatalf("该清掉 1 条，实得 %d", n)
	}
	if l.has("old") {
		t.Error("闲了 11 分钟的那条该被清掉 ✗")
	}
	if !l.has("new") {
		t.Error("才闲 1 分钟的那条**不该**被清掉 ✗")
	}
	if got := l.size(); got != 1 {
		t.Fatalf("清完该剩 1 条，实得 %d", got)
	}
}

func TestCanDropBucket(t *testing.T) {
	idle := time.Hour
	fresh := &bucket{lastSeen: t0}
	if canDropBucket(fresh, t0.Add(time.Minute), idle) {
		t.Error("刚来过的记录不该被清 ✗")
	}
	if !canDropBucket(fresh, t0.Add(2*time.Hour), idle) {
		t.Error("两小时没动静的记录该被清 ✗")
	}
	// 边界：正好等于 idle 时**还没到**清理线（是 `>` 不是 `>=`）
	// 这个边界不重要，但写下来是为了让以后改的人知道现在是什么行为 ✓
	if canDropBucket(&bucket{lastSeen: t0}, t0.Add(idle), idle) {
		t.Error("正好等于 idle 时按现在的实现不该清")
	}
}
