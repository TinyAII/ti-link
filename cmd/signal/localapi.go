// 挂在 **Unix socket** 上的本机端点：给管理面板看运营状态、以及从后台关房间。
//
// 为什么不是"开个公网口"：这些内容（谁在开房间、从哪来）只该服务器主人看得到 ✓
//
// 三个路径：
//
//	/stats        —— 聚合数字（**没有任何能追到某个人的字段**，见 stats.go）
//	/rooms        —— 当前房间列表（房主昵称、开了多久、**打码**的来源地址、账号号）
//	/rooms/close  —— 【写】强制关掉一个房间（POST {room}）
//	/rooms/detail —— 某个房间的详情（GET ?room=XXX）：成员明细 / 游戏 / 密码状态 / 黑名单条数 ✓
//	/rooms/kick   —— 【写】把某个成员请出房间（POST {room, seat}）✓
//	/metrics      —— 历史序列（每 1 秒一个点，最近 6 小时）；概览页的折线图画它 ✓
//	/history      —— 历史房间（开过/进过的房间 + 成员进出时间，只留 30 天）；
//	                 后台「历史房间」页读它 ✓
//
// ═══════════════════════════════════════════════════════════════
// 🩸 为什么是 Unix socket，而不是"只听 127.0.0.1"
// ═══════════════════════════════════════════════════════════════
//
// 原来这儿写的是"不另加鉴权：能连上 127.0.0.1 的人已经在这台机器上了"。
// **那句话是错的** ✗ —— 老板 2026-10-16 一句就问穿了：
//
//	回环地址挡住的是**外网**和**别的机器**，挡不住**同机上的其它进程** ✗
//	而这台机器上还跑着别的项目（nginx / postgres / 博客…）——
//	那些进程里任何一个都能 curl 一下这个端口。
//	读接口无所谓（读走的只是数字和打码地址），可这是**能踢人**的写接口 ✓
//
// 换成 Unix socket 之后，权限由**操作系统**判：socket 文件设成 0600，
// 只有 root（也就是只有信令和面板这两个服务）连得上 ✓
//
// 📌 比"再加一个共享密钥"好在哪：不用发明、不用分发、不用轮换，
// 也不会出现"两个服务用了不同的密钥"这种查半天的问题 ✓
//
// ⚠️ 起不来**不能影响穿透服务**：它只是个附加的小工具，
// 建不出 socket（目录不在、没权限）之类的情况不该让人联不了机 ✗
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

// serveLocalAPI 起本机端点。sockPath 为空就不起。
// serveLocalAPI 起本机端点。sockPath 为空就不起。
//
// ⚠️ 参数名特意用 sockPath（不是 addr）：masklog_test.go 里有一圈扫描，
// 专门禁"日志参数里出现名字像**用户**地址的变量"，而 addr 太泛
// ⇒ 用 sockPath 把"这是自己人的一个文件路径，不是用户地址"说清楚 ✓
func serveLocalAPI(sockPath string) {
	if sockPath == "" {
		return
	}
	srv, ln, err := newLocalAPI(sockPath)
	if err != nil {
		log.Printf("本机端点起不来（不影响联机）：%v", err)
		return
	}
	log.Printf("本机端点：unix://%s（/stats、/rooms、/rooms/detail、/history、/metrics、POST /rooms/close、POST /rooms/kick；权限 0600，只有 root 连得上）", sockPath)
	if err := srv.Serve(ln); err != nil {
		log.Printf("本机端点停了（不影响联机）：%v", err)
	}
}

// newLocalAPI 把 socket 建好、权限设好、handler 挂好 —— **但不开始服务**。
//
// 📌 **为什么非拆成两个函数**：Serve 是**阻塞**的 ⇒ 谁想拿到那个 server
// 去 defer Close，就得等 Serve 返回，而那要等到天荒地老 ✗
// （2026-10-16 踩到：测试调 serveLocalAPI 直接挂死六分钟，就是因为它同步阻塞）
// 产品走 serveLocalAPI；测试走这个 + 自己 go Serve ✓
//
// 🩸 失败时返回 error，**这儿不 log** —— 让调用方决定要不要吼一声，
// 测试才能安静地验"建不出来"这种情况 ✓
func newLocalAPI(sockPath string) (*http.Server, net.Listener, error) {
	// 上次没清干净的 socket 文件会让 bind 直接失败（"address already in use"）✗
	// 它是个 socket 文件、不是普通文件，删掉是安全的 —— 里面没有数据 ✓
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("清不掉旧的 socket 文件: %w", err)
	}

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, nil, err
	}
	// 🩸 **这一步就是这套东西的"鉴权"** —— 少了它，socket 会按 umask 建
	// （常常是 0755）⇒ 同机任何用户都能连，等于白换 ✗
	if err := os.Chmod(sockPath, 0o600); err != nil {
		// 改不了权限就**别开了**：宁可面板取不到数据，
		// 也不能留一个谁都能连的"能踢人"的口子 ✗
		_ = ln.Close()
		_ = os.Remove(sockPath)
		return nil, nil, fmt.Errorf("改不了 socket 权限（不敢留一个谁都能连的口子）: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/stats", statsHandler)
	mux.HandleFunc("/rooms", roomsHandler)
	mux.HandleFunc("/rooms/close", closeRoomHandler)
	mux.HandleFunc("/rooms/detail", roomDetailHandler)
	mux.HandleFunc("/rooms/kick", kickMemberHandler)
	mux.HandleFunc("/metrics", metricsHandler)
	mux.HandleFunc("/history", historyHandler)

	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}, ln, nil
}

// closeRoomHandler 是【写】接口：强制关掉一个房间。
//
// 📌 能走到这儿就说明"调用方连得上那个 0600 的 socket" ⇒ 是本机的 root ✓
// 所以这儿**不再自己加鉴权** —— 但那是"操作系统已经判过了"，不是"没判" ✓
// （跟原来那句"能连 127.0.0.1 就算自己人"不是一回事：那个判的是网卡，这个判的是文件权限）
func closeRoomHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "只接受 POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Room string `json:"room"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeLocalJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不对"})
		return
	}
	if req.Room == "" {
		writeLocalJSON(w, http.StatusBadRequest, map[string]string{"error": "没说关哪个房间"})
		return
	}
	if err := closeRoomByAdmin(req.Room); err != nil {
		writeLocalJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeLocalJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// roomDetailHandler 是【读】接口：某个房间的详情（后台"点进去"那一页）。
//
// 📌 为什么不做成 `/rooms/{id}` 那种路径参数：`net/http` 的 ServeMux 老版本
// 不认路径里的通配 —— 走查询串一行搞定，也不用为了一个端点换路由库 ✓
func roomDetailHandler(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("room")
	if code == "" {
		writeLocalJSON(w, http.StatusBadRequest, map[string]string{"error": "没说看哪个房间"})
		return
	}
	roomsMu.Lock()
	rm := rooms[code]
	roomsMu.Unlock()
	if rm == nil {
		// 房间是内存态 ⇒ "刚关掉"和"从来没开过"在这儿长得一样。
		// 老实说清这个含糊，别让面板那边以为是它自己查错了 ✓
		writeLocalJSON(w, http.StatusNotFound,
			map[string]string{"error": "房间不在了（可能刚关掉，或者本来就没有）"})
		return
	}
	writeLocalJSON(w, http.StatusOK, roomDetailOf(rm, time.Now()))
}

// kickMemberHandler 是【写】接口：把某个成员请出房间。
//
// 跟关房间同一个道理：能连上那个 0600 的 socket 就说明是本机 root ⇒
// 这儿不再自己加鉴权（"操作系统已经判过了"，不是"没判"）✓
func kickMemberHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "只接受 POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Room string `json:"room"`
		Seat *int   `json:"seat"` // 用指针：**0 是合法座位（房主）**，不能跟"没传"混为一谈 ✗
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeLocalJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不对"})
		return
	}
	if req.Room == "" || req.Seat == nil {
		writeLocalJSON(w, http.StatusBadRequest, map[string]string{"error": "要说清哪个房间、哪个座位"})
		return
	}
	if err := kickMember(req.Room, *req.Seat); err != nil {
		writeLocalJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeLocalJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeLocalJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
