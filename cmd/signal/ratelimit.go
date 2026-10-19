// 限流与容量保护。
//
// 这个工具发布出去以后，「有人写个循环刷服务器」是最可能真实发生的攻击 ——
// 服务器本身很轻（只牵线），但连接数被打满就会让正常用户进不来。
package main

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	maxConnsPerIP  = 20   // 单 IP 并发连接上限
	connsPerSecond = 5    // 单 IP 每秒新建连接上限
	maxRooms       = 5000 // 全局房间数上限

	// maxConnsTotal 是**全局并发连接上限**（2026-10-17 补）。
	//
	// 🩸 原来只有"单 IP"那几条 ⇒ 缺一道兜底：来源 IP 一多（多台机器、
	//   僵尸网络、或者这台服务器哪天上了 IPv6），单 IP 的限制就形同虚设 ✗
	//   而没有全局上限的话，连接洪水能直接打满 FD 和内存 ✗
	//   ⚠️ 特别是 allowConn 跑在"读 register、**校验令牌之前**" ⇒
	//      没账号的人也能打这条路径，所以这道兜底更必要 ✓
	//
	// 📌 2000 是拍的：正常规模远到不了（房间上限才 5000，每房也就几个人）；
	//    真到 2000 说明要么火了、要么在被灌 —— 日志里会写清是哪种 ✓
	maxConnsTotal = 2000

	// maxRoomsPerIP 是**单 IP 同时能开几个房间**。
	//
	// 🩸 为什么单独要这一条：其它那几个限制**都是"连接级"的** ——
	// 一个人完全可以在限制之内开一堆房间，把全局配额占满，别人就开不了了 ✗
	//
	// ⭐ 而这条**用户绕不过去** —— 判定在服务器，客户端改也改不动 ✓
	// （对比：查进程名、报端口那些都能靠改客户端绕 ✗）
	//
	// 3 是拍的：正常人自己开房 + 帮朋友试一下，最多两三个 ✓
	// 真需要更多的场景（一人多机、开多个游戏）可以再提 ✓
	maxRoomsPerIP = 3
)

var (
	ipMu     sync.Mutex
	ipLimits = map[string]*ipLimit{}

	// roomsOfIP 记着每个 IP 现在开着几个房间 ✓
	// 跟 ipLimits 共用 ipMu 保护 ✓
	roomsOfIP = map[string]int{}
)

// allowRoom 判断这个 IP 还能不能再开一个房间。
//
// ⭐ 这是全套限制里**唯一一条用户真正绕不过去的** —— 它在服务器侧 ✓
func allowRoom(ip string) bool {
	ipMu.Lock()
	defer ipMu.Unlock()
	if roomsOfIP[ip] >= maxRoomsPerIP {
		return false
	}
	roomsOfIP[ip]++
	return true
}

// releaseRoom 房间关掉时把名额还回去 ✓
//
// 🩸 必须**跟 allowRoom 配对**，而且要在**房间真的被删掉**那条路径上 ——
// 漏了的话名额只减不增，那个用户以后就再也开不了房了 ✗
// （而且这种 bug 特别隐蔽：第一次开房好好的，关了再开就不行了 ✓）
func releaseRoom(ip string) {

	if ip == "" {
		return
	}
	ipMu.Lock()
	defer ipMu.Unlock()
	if n := roomsOfIP[ip]; n > 1 {
		roomsOfIP[ip] = n - 1
	} else {
		// 归零就删掉，别让这张表无界地长 ✓
		delete(roomsOfIP, ip)
	}
}

type ipLimit struct {
	lim    *rate.Limiter
	active int

	// lastSeen 是这个 IP 最近一次来连接的时间。
	//
	// 🩸 为什么需要它：清理时**不能只看 `active == 0`** ✗
	// 那样的话条目被删 = **限流器状态一起没了** ⇒ 攻击者只要
	// "打满 → 等一个清理周期 → 额度又满了"，`connsPerSecond` 就白设了 ✓
	// ⇒ 得**闲置够久**才清（见 idleIPLimitTTL）✓
	lastSeen time.Time
}

// idleIPLimitTTL 一个 IP 要闲置多久才允许被清掉。
//
// 📌 1 小时是拍的：正常人关了程序一小时内不会再连；
// 而攻击者想靠"等清理"重置额度，就得停满一小时 —— 那这个"攻击"也就没意义了 ✓
const idleIPLimitTTL = time.Hour

// canDropIP 判断这条记录能不能清掉。
//
// 🩸 **判据是「闲置够久」，不是「没有活跃连接」** ✗
// 光看 `active == 0` 的话，条目被删 = 限流器状态一起没了
// ⇒ 攻击者"打满 → 等一轮清理 → 额度又满了"，`connsPerSecond` 就白设了 ✓
//
// 📌 抽成函数**是为了能测** —— 测试直接调它 ✓
// 测试里**别把这条判据再抄一遍** ✗ 抄的话测的是抄本、不是真的这份，
// 生产代码改坏了测试也不红（2026-10-15 我第一版就这么写错的 ✓）
func canDropIP(st *ipLimit, now time.Time) bool {
	if st.active > 0 {
		return false // 正在用的，绝不能清 ✗
	}
	return now.Sub(st.lastSeen) > idleIPLimitTTL
}

// allowConn 判断这个 IP 还能不能再建一条连接。
// allowConn 判断"这条新连接放不放"。第二个返回值是**拒绝原因**（放行时空）✓
//
// 📌 给出原因是为了日志能**分清**是"某个 IP 太猛"还是"全局满了" ——
//
//	前者多半是他在猛试、后者才说明我们在被灌，处理方式完全不同 ✓
func allowConn(ip string) (bool, string) {
	// 🩸 全局上限先判：它是**最后一道兜底**，不管来源是谁 ✓
	if statConns.Load() >= maxConnsTotal {
		return false, "全局并发已达上限"
	}
	ipMu.Lock()
	defer ipMu.Unlock()
	st := ipLimits[ip]
	if st == nil {
		st = &ipLimit{lim: rate.NewLimiter(rate.Limit(connsPerSecond), connsPerSecond)}
		ipLimits[ip] = st
	}
	st.lastSeen = time.Now() // 记一笔：这个 IP 还在活动 ✓
	if st.active >= maxConnsPerIP {
		return false, "单 IP 并发已达上限"
	}
	if !st.lim.Allow() {
		return false, "单 IP 建连太快"
	}
	st.active++
	return true, ""
}

func releaseConn(ip string) {
	ipMu.Lock()
	defer ipMu.Unlock()
	if st := ipLimits[ip]; st != nil {
		if st.active > 0 {
			st.active--
		}
		st.lastSeen = time.Now()
	}
}

// cleanIPLimits 定期清掉**闲置够久**的 IP 记录，避免这张表无限增长。
//
// 🩸 注意判据是「**闲置超过 idleIPLimitTTL**」，不是「没活跃连接」✗
// 光看 `active == 0` 会让攻击者"等一轮清理就重置限流额度"✓
// 详见 ipLimit.lastSeen 的说明 ✓
//
// 📌 为什么必须清：服务器**公开在 39000 端口**上，
// 被扫描时每个来源 IP 都会在这张表里留一条 ⇒ 不清就是持续涨 ✗
func cleanIPLimits() {
	for {
		time.Sleep(10 * time.Minute)

		// 先拿一次现在的时间，别在循环里反复调 time.Now() ✓
		now := time.Now()

		ipMu.Lock()
		for ip, st := range ipLimits {
			if canDropIP(st, now) {
				delete(ipLimits, ip)
			}
		}
		ipMu.Unlock()
	}
}
