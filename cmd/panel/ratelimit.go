// 限流：按 IP / 按邮箱数"最近一段时间试了几次"。
//
// 它挡的是"拿脚本猛试密码 / 猛试邀请码"。**不是**账户锁 ——
// 按邮箱锁会让攻击者用"故意试错"把某个人的号锁死（DoS），
// 所以这里的窗口都很短（分钟级），代价可控 ✓
package main

import (
	"sync"
	"time"
)

// bucket 是一个键在某个时间窗里的计数。
type bucket struct {
	count    int
	windowAt time.Time // 这个窗口是什么时候开的
	lastSeen time.Time // 最后一次来是什么时候（清理靠它）
}

// limiter 是固定窗口计数。
//
// 📌 用固定窗口而不是滑动窗口：实现三行，且"边界上能多来一倍"这件事
// 对我们要防的东西（脚本猛试）没有实质影响 ✓ 简单能一眼看懂更重要。
type limiter struct {
	limit int
	per   time.Duration

	mu   sync.Mutex
	hits map[string]*bucket
}

func newLimiter(limit int, per time.Duration) *limiter {
	return &limiter{limit: limit, per: per, hits: map[string]*bucket{}}
}

// allow 记一次，返回放不放行；不放行时给出建议的等待秒数。
func (l *limiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.hits[key]
	if b == nil || now.Sub(b.windowAt) >= l.per {
		l.hits[key] = &bucket{count: 1, windowAt: now, lastSeen: now}
		return true, 0
	}
	b.lastSeen = now
	if b.count >= l.limit {
		return false, l.per - now.Sub(b.windowAt)
	}
	b.count++
	return true, 0
}

// reset 是"成功之后把账销掉"：登录成功就应该不再算失败次数，
// 否则一个正常用户白天登录几次、晚上再登就被自己的历史拖累了。
func (l *limiter) reset(key string) {
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}

// canDropBucket 判断一条记录是不是可以扔了。
//
// 🩸 抽成函数是为了**能测** —— 直接写在清理循环里的话，测试只能把那几行逻辑
// 再抄一遍，而抄的那份永远不会红（cmd/signal/ratelimit.go 上踩过这个坑：
// 第一版测试就是这么写的，把生产代码改坏它照样绿）。
func canDropBucket(b *bucket, now time.Time, idle time.Duration) bool {
	return now.Sub(b.lastSeen) > idle
}

// cleanup 扔掉长期没动静的记录。
//
// 📌 不扔的话这张表只涨不降：每个新 IP / 新邮箱都留一条 ⇒ 慢慢变成内存泄漏。
func (l *limiter) cleanup(now time.Time, idle time.Duration) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for k, b := range l.hits {
		if canDropBucket(b, now, idle) {
			delete(l.hits, k)
			n++
		}
	}
	return n
}

// size / has 只给测试用。
//
// 📌 为什么要 **has** 而不只是 size：只比数量的话，"清对了"和"清错了"
// 给出同一个数字 ⇒ 测试是绿的但什么都没测到 ✗（2026-10-15 反证时踩到：
// 把 canDropBucket 的判据反过来，数量照样是 1，测试照样过。）
func (l *limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.hits)
}

func (l *limiter) has(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.hits[key]
	return ok
}
