// 只读的运营指标：**只数人头，不看人**。
//
// 这里存的每一个数都是聚合量，没有任何一条能追到具体某个用户身上。
//
// 📌 为什么不做"看谁在干什么"：
//
//	① 用不上 —— 这台服务器不转发游戏数据，没有被薅带宽的余地；
//	   防滥用已经有现成的限流（单 IP 并发 / 新建速率 / 房间总数上限）；
//	② 公开分发之后，"记录谁在什么时候连过"本身就是**收集个人信息** ——
//	   那正是我们花力气把日志里的 IP 打码掉要避免的东西 ✗
//
// 真发现有人在刷，做法是**加一条规则**（比如单 IP 房间数上限），而不是去看他是谁。
//
// 📌 读取方式：默认只监听 127.0.0.1，要看的时候走 SSH 隧道
//
//	ssh -L 8080:127.0.0.1:8080 root@<主机>   # 然后本机浏览器开 localhost:8080/stats
//
// 这样不用在服务器上开任何公网端口 ⇒ 运营数据谁都看不到 ✓
package main

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

var startedAt = time.Now()

var (
	statConns      atomic.Int64 // 当前连接数
	statConnsPeak  atomic.Int64 // 连接数峰值（重启清零）
	statRoomsPeak  atomic.Int64 // 房间数峰值
	statRoomsTotal atomic.Int64 // 累计开过多少个房间

	statRejectRate atomic.Int64 // 被限流挡掉的
	statRejectBan  atomic.Int64 // 被黑名单挡掉的
	statBadToken   atomic.Int64 // 登录令牌不认的（无效 / 过期 / 账号停用 / 被强制下线）
	statNoToken    atomic.Int64 // 压根没带令牌的（多半是没登录，或者装了个老版本）
)

// bumpPeak 把峰值顶上去。
//
// 用 CAS 循环而不是加锁：这行代码每次建连接都会走一遍，
// 而它九成九的时候什么都不用做（没破纪录）。
func bumpPeak(peak *atomic.Int64, v int64) {
	for {
		cur := peak.Load()
		if v <= cur || peak.CompareAndSwap(cur, v) {
			return
		}
	}
}

// ---- 埋点：都在这儿，别在业务代码里直接动计数器 ----

func connOpened() { bumpPeak(&statConnsPeak, statConns.Add(1)) }
func connClosed() { statConns.Add(-1) }

// roomOpened 建了个房间。当前房间数不在这儿存 —— 直接数房间表才是真相
// （那儿不会因为哪次忘了减一而慢慢漂走）✓ 这儿只管"累计"和"峰值"。
func roomOpened() {
	statRoomsTotal.Add(1)
	roomsMu.Lock()
	n := int64(len(rooms))
	roomsMu.Unlock()
	bumpPeak(&statRoomsPeak, n)
}

func roomCount() (cur, peak int64) {
	roomsMu.Lock()
	cur = int64(len(rooms))
	roomsMu.Unlock()
	return cur, statRoomsPeak.Load()
}

// statsSnapshot 是 /stats 返回的那份 JSON。
// 字段名用 snake_case —— 手工 curl 的时候好认。
type statsSnapshot struct {
	Uptime       string `json:"uptime"`
	Conns        int64  `json:"conns"`
	ConnsPeak    int64  `json:"conns_peak"`
	Rooms        int64  `json:"rooms"`
	RoomsPeak    int64  `json:"rooms_peak"`
	RoomsTotal   int64  `json:"rooms_total"`
	RejectRate   int64  `json:"reject_ratelimit"`
	RejectBan    int64  `json:"reject_ban"`
	BadToken     int64  `json:"bad_token"`
	NoToken      int64  `json:"no_token"`
	MaxRooms     int    `json:"max_rooms"`
	ConnsPerIP   int    `json:"conns_per_ip"`
	NewConnsPerS int    `json:"new_conns_per_sec"`
}

func statsSnapshotNow() statsSnapshot {
	cur, peak := roomCount()
	return statsSnapshot{
		Uptime:       time.Since(startedAt).Round(time.Second).String(),
		Conns:        statConns.Load(),
		ConnsPeak:    statConnsPeak.Load(),
		Rooms:        cur,
		RoomsPeak:    peak,
		RoomsTotal:   statRoomsTotal.Load(),
		RejectRate:   statRejectRate.Load(),
		RejectBan:    statRejectBan.Load(),
		BadToken:     statBadToken.Load(),
		NoToken:      statNoToken.Load(),
		MaxRooms:     maxRooms,
		ConnsPerIP:   maxConnsPerIP,
		NewConnsPerS: connsPerSecond,
	}
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// 顺便把限流参数一起返回：数字对不上时，一眼能看出"是规则拦的还是没生效"
	_ = enc.Encode(statsSnapshotNow())
}
