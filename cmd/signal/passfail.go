// 房间密码的失败退避。
//
// 🩸 为什么要有这个：密码比较本身是恒定时间的（`passEqual`）✓ 但**没有失败计数** ⇒
// 每次猜测 = 一条新连接，唯一的摩擦是连接级限流（单 IP 每秒 5 次）⇒
// 房间密码常是短数字，一天能试几十万次 ✗
//
// 📌 计数按「**房间 + 来源 IP**」算：
//
//	· 按房间：各房间互不干扰 ✓（一个人在 A 房试错，不该影响他进 B 房）
//	· 按 IP  ：不按 ClientID —— 那东西客户端自报，能随便换 ✗
//	· ⚠️ 代价：同一个 NAT 后面的人共用一个额度（家里/公司/网吧）⇒
//	  所以阈值不能太紧（3 次就开始锁已经够狠，再紧会误伤合租的人 ✗）
//
// 🔒 老板 2026-10-17 拍板：**错 3 次锁 30 秒，之后翻倍，上限 10 分钟**；
//
//	锁定期间**正确密码也进不来**（他选的就是这个 —— 最狠但最干净）。
//
// 🩸🩸 **这三个函数都不加锁**，调用方必须**已经持有 `r.mu`** ——
//
//	`join.go` 里调它们的地方就持着锁 ✓ 我要是这儿再 Lock 一次就是**自己锁死自己** ✗
//	（Go 的 sync.Mutex 不可重入）
//	⇒ 命名沿用项目里已有的约定：加 `Locked` 后缀（见 findBanLocked / broadcastMembersLocked）✓
package main

import (
	"strconv"
	"time"
)

const (
	// passFailFree 是"连续错几次之后开始锁"。
	// 错 1~2 次只记着（正常人也会手滑），**第 3 次就锁 30 秒** ✓
	passFailFree = 3

	// passFailBase 是第一次锁定的时长，之后每次翻倍 ✓
	passFailBase = 30 * time.Second

	// passFailMax 是锁定时长的上限（别让一个人被锁一辈子 —— 那等于把房间锁死）✓
	passFailMax = 10 * time.Minute

	// passFailMaxEntries 是每个房间最多记多少个来源。
	//
	// 🩸 防内存：一个房间开着的时候，别人可以拿一大堆来源 IP 来试错，
	// 每条都往 map 里塞一笔 ⇒ 不设上限就是一条慢速内存泄漏 ✗
	// 超了就**整表清空**（简单、够用：真被刷到这个量，重新计数也不会误伤谁）✓
	passFailMaxEntries = 500
)

// passFail 是"某个来源在某个房间里的连续失败记录"。
type passFail struct {
	n     int       // 连续错了几次（成功一次就清零）
	until time.Time // 锁到什么时候（零值 = 没锁）
}

// passBlockedLocked 查"这个来源现在是不是被锁着"。返回还要等多久，0 = 没锁 ✓
//
// 📌 调用方要在**比密码之前**调它：锁着的时候连密码都不用比 ✓
//
// ⚠️ 必须持着 r.mu 再调（函数名带 Locked）✓
func passBlockedLocked(r *room, ip string, now time.Time) time.Duration {
	f := r.passFails[ip]
	if f == nil || f.until.IsZero() {
		return 0
	}
	if d := f.until.Sub(now); d > 0 {
		return d
	}
	return 0
}

// notePassFailLocked 记一次密码错误；到阈值就上锁。返回这次锁多久（没锁返回 0）✓
//
// ⚠️ 必须持着 r.mu 再调 ✓
func notePassFailLocked(r *room, ip string, now time.Time) time.Duration {
	if r.passFails == nil {
		r.passFails = map[string]*passFail{}
	}
	// 防内存：条目太多就整表清空（见 passFailMaxEntries 的注释）✓
	if _, exists := r.passFails[ip]; !exists && len(r.passFails) >= passFailMaxEntries {
		r.passFails = map[string]*passFail{}
	}

	f := r.passFails[ip]
	if f == nil {
		f = &passFail{}
		r.passFails[ip] = f
	}
	f.n++

	if f.n < passFailFree {
		return 0
	}
	// 错 3 次 ⇒ 30s；4 次 ⇒ 60s；5 次 ⇒ 120s … 上限 10 分钟 ✓
	lock := passFailBase
	for i := passFailFree; i < f.n && lock < passFailMax; i++ {
		lock *= 2
	}
	if lock > passFailMax {
		lock = passFailMax
	}
	f.until = now.Add(lock)
	return lock
}

// clearPassFailLocked 密码对了一次 ⇒ **整笔清零**。
//
// 🩸 这条很重要：不清的话，一个人手滑两次、后来输对了，计数还留着 ✗
// 下次再手滑一次就直接被锁 30 秒 —— 那种"攒着算"的体验很糟 ✗
//
// ⚠️ 必须持着 r.mu 再调 ✓
func clearPassFailLocked(r *room, ip string) {
	delete(r.passFails, ip)
}

// humanWait 把"还要等多久"说成人话（给玩家看的文案里别出现 "1m30.5s"）✓
func humanWait(d time.Duration) string {
	sec := int(d.Round(time.Second) / time.Second)
	if sec <= 0 {
		sec = 1
	}
	if sec < 60 {
		return strconv.Itoa(sec) + " 秒"
	}
	m := sec / 60
	if sec%60 != 0 {
		return strconv.Itoa(m) + " 分 " + strconv.Itoa(sec%60) + " 秒"
	}
	return strconv.Itoa(m) + " 分钟"
}
