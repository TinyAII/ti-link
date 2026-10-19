// 联机逻辑与页面之间的桥接层。
//
// 规则：这里只做「参数校验 + 起停 host/node + 把事件推给页面」，
// 真正的联机实现全在 internal/ 里 —— 这一层薄一点，改界面时不用动这里。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/webview/webview_go"

	"p2plink/internal/game"
	"p2plink/internal/host"
	"p2plink/internal/identity"
	"p2plink/internal/netutil"
	"p2plink/internal/node"
	"p2plink/internal/tunnel"
)

const (
	// defaultSignal 用**域名**而不是 IP。
	// 好处是以后再换服务器只要改 DNS，客户端不用重发。
	// 域名还让 TLS 能校验证书（见 internal/signal 的 TLS 部分）。
	defaultSignal = "mc.tinyaii.cn:39000"
	// 🩸 这里原来写着一个**所有客户端共用**的访问口令（defaultPass）。
	// 它写死在 exe 里、strings 一下就能挖出来 ⇒ 那时的「鉴权」其实是
	// 「你有没有下载过客户端」。2026-10-15 换成账号体系之后它被彻底删掉了：
	// 现在凭据是**登录之后拿到的令牌**，存在数据目录里（见 identity.Token）✓
	// **别往这儿再加任何写死的口令** ✗
	defaultMax = 4
	hardMax    = 100
	// 房间密码的长度区间：留空 = 不设密码；要设就至少 4 位 ——
	// 它同时当隧道加密密钥用，一两位的密码等于没锁。
	minRoomPassLen = 4
	maxRoomPassLen = 32
	// 昵称上限，跟服务器那边对齐
	maxNickLen = 12
	// 房间 ID 的形状：固定前缀 + 6 位数字 + 2 位随机大写字母，形如 TI123456DY。
	//
	// 数字只取 6 位（100 万种），后面那两位随机字母是**用来撑开空间、避免撞号**的；
	// 前缀固定成产品名，扫一眼就知道是自己家的房间号。
	// 字母用满 A-Z：前缀里就有 I，再挑"容易看错的字母"没意义。
	roomPrefix = "TI"
	roomDigits = 6 // 每位 0-9
	roomTail   = 2 // 每位 A-Z
)

// newRoomCode 生成一个房间 ID。形状见上面 roomPrefix 的注释。
func newRoomCode() string {
	b := make([]byte, 0, len(roomPrefix)+roomDigits+roomTail)
	b = append(b, roomPrefix...)
	for i := 0; i < roomDigits; i++ {
		b = append(b, byte('0'+rand.Intn(10)))
	}
	for i := 0; i < roomTail; i++ {
		b = append(b, byte('A'+rand.Intn(26)))
	}
	return string(b)
}

// levelOf 按文案粗判级别，决定界面上状态点的颜色。
func levelOf(msg string) string {
	switch {
	case strings.Contains(msg, "出错"), strings.Contains(msg, "失败"), strings.Contains(msg, "断开"):
		return "err"
	case strings.Contains(msg, "转发中"), strings.Contains(msg, "位朋友"):
		return "ok"
	default:
		return "info"
	}
}

// backend 是暴露给页面 JavaScript 的对象。
type backend struct {
	mu   sync.Mutex
	w    webview.WebView
	hwnd uintptr // 窗口句柄：改主题时要顺手把系统标题栏也染一下
	hst  *host.Host
	run  *node.Runner
	busy bool

	mode     string // 创建房间 / 加入房间 —— 导出日志时写进头部
	room     string
	roomPass string // 房主当前设的房间密码（空 = 没设），改密码后就更新
	logs     logStore
	// lastJoin 记住上一次加入的参数，供「重新连接」重跑（打洞失败留在房间时用）✓
	lastJoin joinParams
	// joinSes 是**当前这一次加入会话**（含它派生出来的每次重连）。
	//
	// 🩸 存它为了两件事（2026-10-18 做自动重连时加的）：
	//   · 「退出房间」要能**打断正在等待的退避重试**，不然点了退出还要干等 20 秒 ✗
	//   · 重连要沿用**同一个本地端口**：游戏里填的是 127.0.0.1:25565，
	//     重连后换个号就等于把地址改了 ✗
	joinSes *joinSession

	// lastFriends 上一次看到的"已连接朋友数"，用来判断人数是变多还是变少。
	// 提示音**只在变多时**响 —— 有人离开不该响（原来只要状态文字里出现
	// 「位朋友连接」就响，结果走一个人也叮一声）。
	lastFriends int

	// serverStop 停掉"本机游戏服务端在不在"的轮询（房间一结束就关）。
	serverStop chan struct{}
}

// cleanNick 收拾一下用户填的昵称：去空白、限长。
// 空字符串是允许的 —— 服务器会给个兜底名（房主 / 玩家 N）。
func cleanNick(s string) string {
	s = strings.TrimSpace(s)
	rs := []rune(s)
	if len(rs) > maxNickLen {
		rs = rs[:maxNickLen]
	}
	return string(rs)
}

// clampAvatar 把头像下标收进有效范围。
//
// 页面传上来的值一律先过这里：越界值传进协议，对面按它查表就会查到别人脸上
// 或直接越界。宁可退回第 0 张，也不能把越界值放出去。
func clampAvatar(n int) int {
	if n < 0 || n >= AvatarCount() {
		return 0
	}
	return n
}

// checkPassLen 只挡「太长」的密码 —— 这是防灌数据用的，两边都要过。
// 不挡「太短」：加入方填的短密码本来就该让服务器回一句「密码不对」，
// 在这儿拦下来反而把话说岔了（明明是密码错，却说成格式问题）。
func checkPassLen(s string) string {
	if len([]rune(s)) > maxRoomPassLen {
		return "房间密码最多 " + strconv.Itoa(maxRoomPassLen) + " 位"
	}
	return ""
}

// checkNewPass 房主设密码时的额外要求：要么留空（不设防），要么至少 4 位。
// 之所以卡下限，是因为这个密码顺带当隧道加密密钥 —— 一两位的密码等于没锁。
func checkNewPass(s string) string {
	if err := checkPassLen(s); err != "" {
		return err
	}
	if s != "" && len([]rune(s)) < minRoomPassLen {
		return "房间密码至少 " + strconv.Itoa(minRoomPassLen) + " 位（留空则不设密码）"
	}
	return ""
}

// addLog 记一条日志。
//
// 界面只拿 human —— 里面不含任何地址（地址只出现在导出的 detail 里，
// 而且那份也是打过码的）。detail 存起来，等用户导出时勾选"含地址"才用。
func (b *backend) addLog(human, detail string) {
	// human 为空 = 只进导出（过程细节，见 logPair 约定）—— 界面不推。
	if human == "" {
		b.logs.addDetailOnly(detail)
		return
	}
	b.logs.add(human, detail)
	b.emit("log", human)
}

// emit 把事件推给页面（必须回到 WebView 主线程）。
func (b *backend) emit(event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	js := fmt.Sprintf("window.onEvent(%q, %s);", event, string(data))
	b.w.Dispatch(func() { b.w.Eval(js) })
}

func (b *backend) setState(msg string) {
	b.emit("state", map[string]string{"text": msg, "level": levelOf(msg)})

	if !currentSettings().SoundOnConnect {
		return
	}
	// 房主侧：**只在有人加进来时**响一声。
	// 判据是人数"变多"（不是"状态文字里出现了某个词"）—— 后者会让
	// 有人离开时也响（2 人房走掉 1 个，状态变成「已有 1 位朋友连接」）。
	if n, ok := friendCount(msg); ok && b.hostGrew(n) {
		playBeep()
	}
}

// hostGrew 房主侧该不该响：**人数比上次多**才算"有人加进来"。
// 顺带把这次的人数记下来，供下次比较。
func (b *backend) hostGrew(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	// 🩸 必须是 **>**（人数"变多"），不是 **!=**（人数"一变就响"）。
	// 写成 != 的话，2 人房走掉 1 个（2 → 1）也会叮一声 —— 而老板定的规矩是
	// 「有玩家加入才响，有人离开不响」。beep_test.go 里专门钉了这条。
	grew := n > b.lastFriends
	b.lastFriends = n
	return grew
}

// friendCount 从房主的状态文字「已有 N 位朋友连接」里抠出 N。
// 抠不出来（别的状态文案）就返回 false —— 那些文案跟"有人加入"无关。
func friendCount(msg string) (int, bool) {
	var n int
	if _, err := fmt.Sscanf(msg, "已有 %d 位朋友连接", &n); err == nil {
		return n, true
	}
	return 0, false
}

// finish 会话结束后的收尾：清状态、通知页面。
func (b *backend) finish() {
	b.endSession(false)
}

// endSession 收掉内部状态。quiet=true 表示**不推 stopped** ——
// 页面要留在房间里（打洞失败可重连那种），推了 stopped 前端就把人踢回选游戏页 ✗
func (b *backend) endSession(quiet bool) {
	b.mu.Lock()
	// 房间结束了，别再探本机服务端了
	if b.serverStop != nil {
		close(b.serverStop)
		b.serverStop = nil
	}
	b.hst = nil
	b.run = nil
	b.busy = false
	b.mu.Unlock()
	if quiet {
		return
	}
	b.setState("已停止")
	b.emit("stopped", "")
}

/* ==================== 房主 ==================== */

// CreateRoom 由页面调用：房主建房间（支持多人）。
// nick 是自己在成员列表里的名字，roomPass 是房间密码（留空 = 不设）。
// CreateRoom 建房。
//
// proto 是这个游戏的协议（"tcp" / "udp" / "both"），由页面按 GAMES 里的 proto 传进来。
// 🩸 Go 这边看不到当前选了哪个游戏（curGameName 是前端状态），只能让页面告诉它 ✓
//
// 参数**分开传，谁也别从谁推**：
//
//	proto      ⇒ 决定**转发**开哪几路（"udp"/"both" 时额外开一路 UDP）
//	probeModes ⇒ 怎么探"游戏开没开"，**列表**，按顺序试、任一成功即算开着
//	gameName   ⇒ 哪款游戏（校验和 proc 探测都要认准它）
//
// 🩸 为什么不能互相推：星露谷是活例子 —— 转发要 both（官方说防火墙
// TCP+UDP 都放行），但它**实际只听 UDP** ⇒ 探测得另配 ✗
// 一件事推另一件，迟早对不上（2026-10-15 栽过一次 ✓）
//
// 📌 进程名关键词**不在这儿传** —— 那在 internal/game 里（GUI 和命令行共用一份 ✓）
//
// 📌 老板定的原则：**能 TCP 就 TCP，进程是没办法了才看** ✓
// 所以默认 probeModes 只有 ["tcp"]，要加 "proc" 得页面明确传过来 ✓
// ⛔ 这里曾经有个「建房前校验端口上是不是那款游戏」的逻辑，**已经去掉了**。
//
// 为什么去掉（2026-10-15，别人指出来的）—— 它挡不住有心的，只挡得住不会改文件名的：
//
//	把 evil.exe 改名成 terraria.exe  ⇒ 过了 ✗
//	伪造 java 命令行参数            ⇒ 过了 ✗
//	我当时还留了个 -allow-any-port 开关 ⇒ 更等于没有 ✗
//
// 而代价是实打实的：游戏改名、权限读不到进程名、随机端口的游戏（我的世界）
// 都可能被**误拦** —— 正常用户被挡，坏人手一改就过 ✗
//
// ⇒ 最要命的是它**看起来像防护**，给的是**虚假的安全感**，那比没有更糟 ✓
//
// 📌 **所以"这个端口能不能用来联机"由用户自己判断，我们不假装能替他把关。**
//
// 🩸 但**别把"查进程"一起删了** —— 那个「游戏开没开」的状态灯还在用它
// （见下面 probeOnce 的 "proc" 分支 / internal/game 的 VerdictFor）✓
// **灯是显示，不是限制，两码事** ✓
func (b *backend) CreateRoom(port, extraPort, maxSize, nick, roomPass string, avatar int,
	proto string, probeModes []string, gameName string) string {

	// ⭐ 端口先过一遍策略：**1–1023 和几个高危端口不放行** ✓
	//
	// 🩸 这**不是安全边界**，只是"挡一下顺手" —— 真想滥用的人换个端口照样跑 ✗
	// 详细说明见 internal/netutil/ports.go 开头 ✓
	//
	// 📌 我们的游戏全在 1024 以上（25565 / 7777 / 24642 / 随机高端口），不会误伤 ✓
	if p, err := strconv.Atoi(port); err == nil {
		if msg := netutil.CheckPort(p); msg != "" {
			return msg
		}
	}
	b.mu.Lock()
	if b.busy {
		b.mu.Unlock()
		return "已经在运行中了"
	}
	b.mu.Unlock()

	port = strings.TrimSpace(port)
	if port == "" {
		port = "25565"
	}
	n, err := strconv.Atoi(strings.TrimSpace(maxSize))
	if err != nil || n <= 0 {
		n = defaultMax
	}
	if n > hardMax {
		n = hardMax
	}

	nick = cleanNick(nick)
	roomPass = strings.TrimSpace(roomPass)
	if err := checkNewPass(roomPass); err != "" {
		return err
	}

	code := newRoomCode()

	// 新会话：日志清空重来（免得上一局的混进来）
	b.logs.reset()
	b.mu.Lock()
	b.mode, b.room, b.roomPass = "创建房间", code, roomPass
	b.lastFriends = 0 // 人数计数也跟着重来，免得拿上一局的人数比
	b.mu.Unlock()

	// 盯着本机游戏服务端在不在，结果推给「房间信息」页那个灯（见 watchLocalServer）。
	// 不在也照样允许建房 —— 房主常常是先开工具、再开游戏服务端。
	stop := make(chan struct{})
	b.mu.Lock()
	b.serverStop = stop
	b.mu.Unlock()
	go b.watchLocalServer(probeSpec{
		Modes:    probeModes,
		Addr:     "127.0.0.1:" + port,
		GameName: gameName,
	}, stop)

	h := host.New(host.Config{
		SignalAddr:  defaultSignal,
		StunServers: currentSettings().StunServers, // 空 = 用内置默认（见 netutil.DefaultStunServers）
		Room:        code,
		Token:       identity.Token(),
		RoomPass:    roomPass,
		Nick:        nick,
		Avatar:      clampAvatar(avatar),
		ClientID:    identity.ClientID(),
		MaxSize:     n,
		Local:       "127.0.0.1:" + port,
		EnableUDP:   proto == "udp" || proto == "both",
		// 第二路（饥荒洞穴那类）。空 / 非法 / 敏感端口 ⇒ 0 = 不开 ✓
		// 🩸 这里再过一遍 CheckPort：前端传来的东西**不能信** ✗
		GamePort2: secondPortOf(extraPort),
		// 游戏名报给服务器：后台看板显示 / 按游戏筛选 ✓
		// 🩸 自报字段，只是展示（见 host.Config.Game 的说明）✗
		Game: gameName,
	})
	h.OnLog = func(human, detail string) { b.addLog(human, detail) }
	h.OnState = func(s string) { b.setState(s) }
	h.OnMembers = func(ms []host.Member) { b.emitMembers(ms) }

	b.mu.Lock()
	b.hst = h
	b.busy = true
	b.mu.Unlock()

	go func() {
		// 🩸 房主点「关闭房间」时，信令连接是我们**自己关的**，recv 会以
		// "use of closed network connection" 报错 —— 那是正常收尾，不是故障。
		// 不区分的话，主动关房间的人会看到一个「连接断开了」的错误弹窗。
		// 真正的断线（网络没了）仍然照报。
		if err := h.Run(); err != nil && !errors.Is(err, host.ErrStopped) {
			b.emit("log", "结束："+err.Error())
			b.emit("fail", err.Error())
		}
		b.finish()
	}()

	b.emit("room", map[string]string{"code": code, "max": strconv.Itoa(n), "pass": roomPass})

	// 记住昵称和上次端口，下次自动填上
	if nick != "" {
		_ = updateSettings(func(s *Settings) { s.Nick = nick })
	}
	if currentSettings().RememberPort {
		_ = updateSettings(func(s *Settings) { s.LastPort = port })
	}
	return ""
}

// RememberGamePort 由页面调用：记下"某个游戏上次用过的端口"。
//
// 为什么要**页面**来告诉 Go 是哪个游戏：Go 这边看不到当前选了哪个游戏 ——
// `curGameName` 是前端的状态 ✓ 所以只能让页面在建房成功时叫一声 ✓
//
// 空游戏名 / 空端口 / 没开「记住」⇒ 直接忽略（宁可不记，也别写一条没用的进去）✓
func (b *backend) RememberGamePort(game, port string) string {
	if game == "" || port == "" || !currentSettings().RememberPort {
		return ""
	}
	err := updateSettings(func(s *Settings) {
		if s.PortByGame == nil {
			s.PortByGame = map[string]string{}
		}
		s.PortByGame[game] = port
	})
	if err != nil {
		return err.Error()
	}
	return ""
}

// watchLocalServer 盯着本机游戏服务端在不在，把结果推给「房间信息」页那个状态灯。
//
// 为什么不是"建房时探一次就完事"：房主往往是先开工具、再开游戏服务端。
// 只探一次的话，灯会永远停在建房那一刻的结果 —— 房主把服务端开起来了它也不变，那这灯就没用了。
// 所以隔几秒重探一次，开起来就变绿。
//
// probeSpec 描述「怎么探这个游戏开没开」。
//
// 📌 Modes 是个**列表**，按顺序试、**任一成功就算开着** ✓
//
//	"tcp"  连一下它的 TCP 端口
//	"proc" 查本机这个端口是不是目标游戏在听
//	（空列表 = 什么都不试 ⇒ 界面如实说"测不了"）
//
// 🔑 老板定的原则：**能 TCP 就 TCP，进程是没办法了才看** ✓
// 所以默认只配 "tcp"；要加 "proc" 得明确写出来 ✓
// 而"两路都留着"比"二选一"稳：
//
//	TCP 通了 ⇒ 直接绿灯，不用管进程名叫啥（**玩家改了文件名也不怕** ✓）
//	TCP 不通 ⇒ 再看进程，兜住"TCP 探不到但游戏确实开着"的情况 ✓
type probeSpec struct {
	Modes []string // tcp / proc
	Addr  string   // 本机游戏地址，如 127.0.0.1:24642
	// GameName 是"proc 那一路要认准哪款游戏" ✓
	// 进程名关键词不在这儿传 —— 那在 internal/game 里（GUI 和命令行共用一份 ✓）
	GameName string
}

// probeTCP 就是原来那套：连一下 TCP 端口 ✓
func probeTCP(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 1500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// probeOnce 探一次，按 Modes 的顺序试。
//
// known=false ⇒ **这次给不出确定答案** ⇒ 界面说"测不了" ✓
//
// 🩸 语义上有个要紧的区分（老板专门提的）：
//
//	"tcp"  连不上 ⇒ 这是**确定**的"没开" ✓（decided）
//	"proc" 查不动（权限之类）⇒ 这是**不知道**，**不是**"没开" ✗
//
// ⇒ 所以 proc 失败时**不留痕迹**、接着试下一种；
//
//	而只要有**任何一种方式拿到了确定答案**，就按它下结论 ✓
func (b *backend) probeOnce(sp probeSpec) (up, known bool) {
	if len(sp.Modes) == 0 {
		return false, false
	}
	decided := false
	for _, m := range sp.Modes {
		switch m {
		case "tcp":
			// TCP 的答案永远是确定的：连不上就是没开 ✓
			decided = true
			if probeTCP(sp.Addr) {
				return true, true
			}

		case "proc":
			// 查这个端口上是不是**那款游戏**（内部分别查 TCP / UDP 两张表 ✓）
			// ⚠️ 这儿要认准某一款 —— 灯是给"用户选的那个游戏"看的 ✓
			v := game.VerdictFor(game.AddrPort(sp.Addr), sp.GameName)
			if !v.Known {
				continue // 说不准 ⇒ 当作"不知道"，别下结论 ✗
			}
			decided = true
			if v.Game != "" {
				return true, true
			}
		}
	}
	if !decided {
		return false, false // 一种确定答案都没拿到 ⇒ "测不了" ✓
	}
	return false, true // 试到的都说"没有" ⇒ 确定没开 ✓
}

// watchLocalServer 盯着本机游戏服务端在不在，把结果推给「房间信息」页那个状态灯。

// addrPort 从 "127.0.0.1:24642" 里取端口号 ✓（取不到返回 0）
func addrPort(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return 0
	}
	return n
}

// watchLocalServer 盯着本机游戏服务端在不在，把结果推给「房间信息」页那个状态灯。
//
// 为什么不是"建房时探一次就完事"：房主往往是先开工具、再开游戏服务端。
// 只探一次的话，灯会永远停在建房那一刻的结果 —— 房主把服务端开起来了它也不变，那这灯就没用了。
// 所以隔几秒重探一次，开起来就变绿。
//
// 日志**只在出问题、或问题解决时**写一条 —— 每 3 秒写一条会把运行日志刷爆 ✗
//
// 探测方式按 probeSpec.Mode 选（见上面的说明）✓
// 判断不了的时候发 up=null，界面如实说"测不了" —— 绝不瞎报"连不上" ✗
func (b *backend) watchLocalServer(sp probeSpec, stop <-chan struct{}) {
	// 🩸 一种方式都没配 ⇒ 这个游戏**压根不检测** ⇒ 发一次"测不了"就**收工** ✓
	//
	// ⚠️ 这里必须 return，不能"发完继续轮询"：
	// 没配方式时 probeOnce 永远返回 known=false，进了循环就是**每 3 秒空转、
	// 永远不退出** ✗ 同步调用它的地方会直接死锁
	// （2026-10-15 重写这段时把原来那句 `return` 弄丢了，测试当场卡死 5 分钟 ✓）
	if len(sp.Modes) == 0 {
		b.emit("server", map[string]any{"up": nil})
		return
	}

	up, known := b.probeOnce(sp)
	prev, prevKnown := up, known

	if !known {
		b.emit("server", map[string]any{"up": nil})
	} else {
		b.emit("server", map[string]bool{"up": up})
		if !up {
			b.emit("log", "提醒：本机没检测到游戏服务端（"+sp.Addr+"）—— 请先开好再让朋友进来")
		}
	}

	for {
		select {
		case <-stop:
			return
		case <-time.After(3 * time.Second):
		}

		up, known = b.probeOnce(sp)
		if !known {
			// 这一刻查不动了（权限变了之类）⇒ 退回"测不了"，别硬报 ✗
			// 但循环**继续** —— 下次可能又查得动了 ✓
			if prevKnown {
				prevKnown = false
				b.emit("server", map[string]any{"up": nil})
			}
			continue
		}
		if !prevKnown || up != prev {
			prev, prevKnown = up, true
			b.emit("server", map[string]bool{"up": up})
			if !up {
				b.emit("log", "提醒：本机没检测到游戏服务端（"+sp.Addr+"）—— 请先开好再让朋友进来")
			} else {
				b.emit("log", "本机游戏服务端已就绪（"+sp.Addr+"），朋友可以进来了")
			}
		}
	}
}

// emitMembers 把名单推给页面。
//
// label 是**纯昵称**（没起名就是空串）+ seat 座位号 —— 界面上「房主 / 玩家」
// 那截前缀由页面自己拼（它要分色渲染），规则见 proto.DisplayName。
func (b *backend) emitMembers(ms []host.Member) {
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		out = append(out, map[string]any{
			"id": m.ID, "label": m.Label, "seat": m.Seat, "host": m.Host, "avatar": m.Avatar,
			"game": m.Game, // 他报的游戏（自报字段，只用来显示）✓
		})
	}
	b.emit("members", out)
}

// SetRoomPass 由页面调用：房主改房间密码。返回错误文案，空 = 成功。
// 已经在房间里的朋友不受影响 —— 新密码只管之后再来的人。
func (b *backend) SetRoomPass(pass string) string {
	pass = strings.TrimSpace(pass)
	if err := checkNewPass(pass); err != "" {
		return err
	}
	b.mu.Lock()
	h := b.hst
	b.mu.Unlock()
	if h == nil {
		return "房间还没建好"
	}
	if err := h.SetRoomPass(pass); err != nil {
		return err.Error()
	}
	b.mu.Lock()
	b.roomPass = pass
	b.mu.Unlock()
	b.emit("roompass", pass)
	return ""
}

// GetBanList 由页面调用：取当前黑名单（打开「管理黑名单」时拉一次）。
func (b *backend) GetBanList() []map[string]string {
	b.mu.Lock()
	h := b.hst
	b.mu.Unlock()
	if h == nil {
		return []map[string]string{}
	}
	bs := h.BanList()
	out := make([]map[string]string, 0, len(bs))
	for _, x := range bs {
		out = append(out, map[string]string{"key": x.Key, "label": x.Label, "short": x.Short})
	}
	return out
}

// Unban 由页面调用：把某人移出黑名单。返回错误文案，空 = 成功。
func (b *backend) Unban(key string) string {
	b.mu.Lock()
	h := b.hst
	b.mu.Unlock()
	if h == nil {
		return "房间还没建好"
	}
	if err := h.Unban(key); err != nil {
		return err.Error()
	}
	return ""
}

// Kick 由页面调用：把某人踢出房间（TA 还能重新加入）。
func (b *backend) Kick(id string) {
	b.mu.Lock()
	h := b.hst
	b.mu.Unlock()
	if h != nil {
		h.Kick(id)
	}
}

// Ban 由页面调用：把某人拉黑（TA 不能再进这个房间）。
func (b *backend) Ban(id string) {
	b.mu.Lock()
	h := b.hst
	b.mu.Unlock()
	if h != nil {
		h.Ban(id)
	}
}

/* ==================== 朋友 ==================== */

// pickFreePort 从 prefer 开始找一个能用的本机端口。
// 朋友端不需要用户操心端口：25565 空着就用它，被占（比如本机也开着服务端）就往后顺延。
func pickFreePort(prefer int) int {
	for p := prefer; p < prefer+100 && p < 65535; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			ln.Close()
			return p
		}
	}
	return prefer
}

// JoinRoom 由页面调用：朋友加入房间。本机端口自动挑，用户不用管。
// nick 是自己在成员列表里的名字，roomPass 是房主给的房间密码（房主没设就留空）。
// gameName 是自己这边在开哪款游戏 —— 报给服务器只是给后台看板用（自报字段，不是防线）✓
func (b *backend) JoinRoom(code, nick, roomPass string, avatar int, portHint, extraHint string,
	proto string, gameName string) string {
	b.mu.Lock()
	if b.busy {
		b.mu.Unlock()
		return "已经在运行中了"
	}
	b.mu.Unlock()

	code = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), " ", ""))
	if code == "" {
		return "请先输入房间 ID"
	}

	nick = cleanNick(nick)
	roomPass = strings.TrimSpace(roomPass)
	if err := checkPassLen(roomPass); err != "" {
		return err
	}

	jp := joinParams{
		code: code, nick: nick, roomPass: roomPass, avatar: avatar,
		portHint: portHint, extraHint: extraHint, proto: proto, gameName: gameName,
	}
	local, local2 := pickJoinAddrs(jp)

	// 🩸 这里**故意不发** "local"。
	// "local" 一到页面，页面就跳进房间页 —— 而此刻服务器还没收下我们：
	// 密码不对 / 房间不存在的时候，会先跳进房间页、再被 stopped 弹回上一层，
	// 看着就像"密码错了反而先进了房间"。（老板碰到的就是这个。）
	//
	// 判断"服务器收下了"只能看**收到第一份成员名单**：
	//   · 不能看状态 —— 节点报 StateWaiting 是在**发出注册之后立刻**报的，
	//     服务器的回话还没到，密码错的人也走得到那一步；
	//   · 名单则是服务器**接受之后**才推的（服务端 join.go 里 append 完才 broadcast）。
	// ⇒ 所以 emit local 在 node 的 OnMembers 里（见 newJoinRunner）✓

	// 新会话：日志清空重来
	b.logs.reset()
	b.mu.Lock()
	b.mode, b.room, b.roomPass = "加入房间", code, roomPass
	// 记住这组参数：「重新连接」按钮靠它重跑一遍，不用用户再填 ✓
	b.lastJoin = jp
	b.mu.Unlock()

	// 记住昵称，下次自动填上（房间密码不记 —— 那是房主的秘密，不该落盘）
	if nick != "" {
		_ = updateSettings(func(s *Settings) { s.Nick = nick })
	}
	return b.beginJoin(jp, local, local2, false)
}

// pickJoinAddrs 挑本机给游戏连的地址（主路 + 第二路）。
//
// 🩸 只在**新的一次加入**时调用。重连沿用上一次挑好的 —— 游戏里填的是
// `127.0.0.1:25565`，重连后换个端口就等于把地址改了 ✗
func pickJoinAddrs(jp joinParams) (local, local2 string) {
	// 本机隧道入口的端口：**从这个游戏的默认端口开始**找空闲的 ✓
	//
	// 🩸 原来写死 25565 —— 那是我的世界的默认值。玩泰拉瑞亚的人看到
	// `127.0.0.1:25565` 会以为搞错了 ✗（其实能连上，但看着别扭）
	// 🩸 必须由**页面**告诉 Go 该从哪个端口找：Go 那边看不到当前选了哪个游戏
	// （curGameName 是前端的状态）✓
	want := 25565
	if n, err := strconv.Atoi(strings.TrimSpace(jp.portHint)); err == nil && n > 0 && n < 65536 {
		want = n
	}
	port := pickFreePort(want)
	local = fmt.Sprintf("127.0.0.1:%d", port)

	// 第二路（饥荒洞穴那类）的本地端口。
	//
	// 🩸 要**避开已经选中的主端口**：《饥荒》平时是 10999(主) + 10998(穴)，
	//    但如果 10998 被占，pickFreePort 会顺延到 10999 ⇒ **跟主端口撞上** ✗
	//    ⇒ 用 pickFreePortExcept 跳过它 ✓
	//
	// 📌 本机监听哪个号其实无所谓（对面不认识它，隧道那头是房主连自己本机游戏），
	//    只要不撞、能听就行 ✓ 这里尽量贴着它习惯的位置（主端口 - 1）✓
	if extra, err := strconv.Atoi(strings.TrimSpace(jp.extraHint)); err == nil && extra > 0 && extra < 65536 {
		if p := pickFreePortExcept(extra, port); p > 0 {
			local2 = fmt.Sprintf("127.0.0.1:%d", p)
		}
	}
	return local, local2
}

/* ==================== 断线自动重连 ==================== */

const (
	// maxRetryJoin 是断线之后最多**补连**几次（不含第一次）。
	//
	// 📌 5 次是个折中：
	//
	//	网络抖一下（两三秒就好）⇒ 第 1~2 次就回来了 ✓
	//	真断网（拔网线、关 Wi-Fi）⇒ 试 5 次也就五十来秒，再多用户只会觉得卡死 ✗
	maxRetryJoin = 5
	// 退避起点，之后每次翻倍
	retryBase = 2 * time.Second
	// 退避上限：2 → 4 → 8 → 16 → 20（封顶）✓
	retryMaxWait = 20 * time.Second
	// retryWindow 是整个重连过程的**总时限**。
	//
	// 🩸 光有次数上限不够：每轮 Start 自己还要耗时间（连服务器最多 6 秒、
	// 等房主候选最多 60 秒…），5 轮叠起来可能十几分钟 ——
	// 用户面对的界面就是"一直转圈"，比直接报错还难受 ✗
	retryWindow = 3 * time.Minute
)

// retryDelay 第 n 次重连前等多久（n 从 1 起）：2 / 4 / 8 / 16 / 20 秒。
//
// 为什么"越等越久"而不是"每 2 秒猛敲"：
//
//	掉线多半是网络本身出问题（路由器重启、Wi-Fi 断开）。这时候高频重试
//	帮不上忙，只会把运行日志刷满、还让界面一直在动 ✗
//	退避既能快速接住"抖一下"，又不会在真断网时折腾 ✓
func retryDelay(n int) time.Duration {
	if n < 1 {
		return retryBase
	}
	shift := n - 1
	if shift > 5 { // 防溢出：2s << 5 = 64s 已经超上限了
		return retryMaxWait
	}
	if d := retryBase << shift; d > 0 && d <= retryMaxWait {
		return d
	}
	return retryMaxWait
}

// waitTimeoutOf 等房主候选的上限。
//
// 第一次给宽一点（180 秒）：房主可能还没开机，挂着等很正常 ✓
// 重连只给 60 秒 —— 刚刚才连上过，房主就在那儿；等太久会让"自动重连"看着像卡死 ✗
func waitTimeoutOf(first bool) time.Duration {
	if first {
		return 180 * time.Second
	}
	return 60 * time.Second
}

// joinSession 是"一次加入"的共享状态 —— 横跨**第一次连接 + 后面若干次重连**。
//
// 🩸 为什么必须跨重连保留（而不是每次重连重新算一遍）：
//
//	① **本地端口不能变**。游戏里填的是 127.0.0.1:25565，重连后要是换了个号，
//	   用户得回游戏里改地址 —— 那就不叫"自动"了 ✗
//	② **"已进房"只报一次**。重连成功不该再响一声提示音、也不该把页面又跳一遍 ✗
//	③ **要能被打断**。用户点「退出房间」时如果正好在等退避（最长 20 秒），
//	   光靠 r.Stop() 停不下来 —— 那一刻压根没有 Runner 在跑 ✗
type joinSession struct {
	jp     joinParams
	local  string // 本机给游戏连的地址（主路），重连沿用
	local2 string // 第二路（空 = 不开）

	mu     sync.Mutex
	stop   chan struct{}
	closed bool
	saidHi bool // 「已进房」是否已经报过
}

func newJoinSession(jp joinParams, local, local2 string, saidHi bool) *joinSession {
	return &joinSession{jp: jp, local: local, local2: local2,
		stop: make(chan struct{}), saidHi: saidHi}
}

// cancel 打断重连循环（幂等：连点两次「退出」不会 panic）。
func (s *joinSession) cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.stop)
}

func (s *joinSession) canceled() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// hello 返回 true 表示这是**第一次进房**（只有这一次才响提示音 / 推地址跳页）。
func (s *joinSession) hello() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saidHi {
		return false
	}
	s.saidHi = true
	return true
}

// beginJoin 起一次加入会话（含之后可能的自动重连），立刻返回。
//
// saidHi=true 表示"这已经不是第一次进这个房了"（「重新连接」按钮、或重连）。
func (b *backend) beginJoin(jp joinParams, local, local2 string, saidHi bool) string {
	b.mu.Lock()
	if b.busy {
		b.mu.Unlock()
		return "已经在运行中了"
	}
	// 🩸 先把新句柄装好，再打断旧的：反过来的话旧循环可能在两者之间收尾，
	//    把刚设好的 busy 又清回 false ✗（另外 finish 那边还有 endSessionThis 兜底）
	old := b.joinSes
	ses := newJoinSession(jp, local, local2, saidHi)
	b.joinSes = ses
	b.busy = true
	b.mu.Unlock()

	if old != nil {
		old.cancel()
	}
	go b.joinLoop(ses)
	return ""
}

// joinEnd 是"这一轮跑完了，接下来怎么办"的四种归宿。
type joinEnd int

const (
	joinEndStop  joinEnd = iota // 用户停的 / 正常结束 ⇒ 收尾
	joinEndPunch                // 打洞不通 ⇒ 留在房间 + 横幅 + 手动重连
	joinEndFatal                // 再试也没用 ⇒ 报错退出
	joinEndBreak                // 断了 ⇒ 自动重连
)

// classifyJoinEnd 是 joinLoop **唯一**的判据来源。
//
// 🩸 为什么抽成函数而不是写在循环里：得让测试能钉住它。
// 这套"三种错误三样去处"的规矩很容易被后来的改动搅乱（比如把 FatalError
// 的判定挪到 PunchError 前面、或者漏掉 ErrStopped）—— 写在循环里就只能靠
// 真断一次网来发现 ✗ 抽出来之后，一个纯函数测试就够 ✓
//
// 判据顺序也有讲究：**先 Punch 再 Fatal**。两者不会互相误判（类型不同），
// 但顺序固定下来，以后谁加新类型都知道往哪儿塞。
func classifyJoinEnd(err error) joinEnd {
	if err == nil || errors.Is(err, node.ErrStopped) {
		return joinEndStop
	}
	var pe *node.PunchError
	if errors.As(err, &pe) {
		return joinEndPunch
	}
	var fe *node.FatalError
	if errors.As(err, &fe) {
		return joinEndFatal
	}
	return joinEndBreak
}

// joinLoop 跑一次加入会话；中途断开就按退避自动重连。
//
// 收尾分四种，**别混**（这套分类是 2026-10-18 定的，判据见 classifyJoinEnd）：
//
//	Stop  ⇒ 正常收尾（页面退回上一层）
//	Punch ⇒ 留在房间 + 横幅 + 手动重连（打不通 ≠ 房间结束）
//	Fatal ⇒ 报错退出（密码不对 / 被踢 / 端口被占，重试答案一样）
//	Break ⇒ 自动重连，次数或总时限用完才放弃
func (b *backend) joinLoop(ses *joinSession) {
	attempt := 0
	deadline := time.Now().Add(retryWindow)

	for {
		r := b.newJoinRunner(ses, attempt == 0)
		b.mu.Lock()
		b.run = r
		b.busy = true
		b.mu.Unlock()

		err := r.Start()

		// 这一轮已经结束了，先把句柄摘掉：等下可能要等退避（最长 20 秒），
		// 那段时间里它就是个死对象，留着会被「退出房间」摸到 ✗
		b.mu.Lock()
		b.run = nil
		b.mu.Unlock()

		// 用户点了「退出房间」，或者已经开了新的一次 ⇒ 立刻收手
		if ses.canceled() {
			b.endSessionThis(ses, false)
			return
		}

		switch classifyJoinEnd(err) {
		case joinEndStop:
			b.endSessionThis(ses, false)
			return

		case joinEndPunch:
			// 打洞没打通：留在房间 + 横幅 + 手动重连（老板 2026-10-18 定的）
			//
			// 🩸 这里**不自动重连**：打洞内部已经换端口重协商过一整轮了，
			//    原地再来一遍只是让用户多等一轮 25 秒，结果九成还是一样 ✗
			msg := err.Error()
			b.emit("log", "没连上房主："+msg)
			b.setState("没连上房主，可以重试")
			b.markServerFailed()
			b.emit("punchfail", msg)
			b.endSessionThis(ses, true) // 收状态，但**别**推 stopped（页面留在房间）
			return

		case joinEndFatal:
			// 错误不能只躺在「运行日志」里 —— 那个标签页没人会主动去翻，
			// 等于没提示。推一份给页面弹窗。
			msg := fatalMsg(err, attempt)
			b.emit("log", "结束："+msg)
			b.emit("fail", msg)
			b.endSessionThis(ses, false)
			return
		}

		// joinEndBreak：断了（信令抖动、服务器重启、半路掉网）⇒ 自动重连 ✓
		attempt++
		if attempt > maxRetryJoin || time.Now().After(deadline) {
			b.giveUp(ses, err, attempt-1)
			return
		}

		wait := retryDelay(attempt)
		reason := breakReason(err)
		b.emit("log", fmt.Sprintf("%s，正在自动重连（第 %d/%d 次）", reason, attempt, maxRetryJoin))
		b.setState(fmt.Sprintf("%s，重连中 %d/%d…", reason, attempt, maxRetryJoin))
		b.emit("reconnecting", map[string]any{
			"attempt": attempt, "max": maxRetryJoin,
			"wait": int(wait / time.Second), "reason": reason,
		})
		// 原始错误只进导出：`read tcp ...: i/o timeout` 这种给人看没意义 ✓
		b.addLog("", fmt.Sprintf("自动重连第 %d 次（等 %s）；上一轮原因：%v", attempt, wait, err))

		select {
		case <-ses.stop:
			b.endSessionThis(ses, false)
			return
		case <-time.After(wait):
		}
	}
}

// newJoinRunner 按会话参数造一个 node.Runner，并把事件接到界面上。
//
// 🩸 每次重连都**新建一个** Runner：Runner 是一次性的（stop 通道、连接、
// 游戏流计数都不会复位），把旧的翻出来重跑会带着上一轮的残渣 ✗
//
// first=true 表示这是本次会话的第一次尝试（决定"等房主多久"和"要不要报已连上"）。
func (b *backend) newJoinRunner(ses *joinSession, first bool) *node.Runner {
	jp := ses.jp
	r := node.New(node.Config{
		SignalAddr:  defaultSignal,
		StunServers: currentSettings().StunServers,
		Room:        jp.code,
		Token:       identity.Token(),
		RoomPass:    jp.roomPass,
		Nick:        jp.nick,
		Avatar:      clampAvatar(jp.avatar),
		ClientID:    identity.ClientID(),
		Local:       ses.local,
		EnableUDP:   jp.proto == "udp" || jp.proto == "both",
		// 第二路的本地监听。空 = 不开 ✓
		// 📌 真正开不开还看**房主报没报第二路**（node 收到 TypePeer 才知道）✓
		Local2:      ses.local2,
		Game:        jp.gameName, // 给后台看板用（自报字段，只当展示）✓
		WaitTimeout: waitTimeoutOf(first),
	})
	r.OnLog = func(human, detail string) { b.addLog(human, detail) }
	// 实时状态（延迟/丢包/v4-v6）：连上之后每 2 秒推一次，铺在「房间信息」页 ✓
	r.OnLive = func(s tunnel.LiveStat) {
		// 📌 发**结构化字段**而不是拼好的句子：界面要分行列出，一句现成的话没法拆 ✗
		b.emit("live", map[string]any{
			"ok": s.OK, "family": s.Family, "rtt": s.RTTms, "loss": s.LossPct,
		})
	}
	// 重连之后**重新连上**要告诉页面一声 —— 它得把"正在重连"的横幅收掉 ✓
	var upOnce sync.Once
	r.OnState = func(s node.State, msg string) {
		b.setState(msg)
		// 玩家侧的「游戏连接」指示灯。
		//
		// 🩸 不能照搬房主那套（探 127.0.0.1:<端口>）：玩家那边这个端口是
		// **自己的转发监听**，隧道没建好它就已经在监听了 ⇒ 探出来永远是亮的，
		// 房主游戏没开也显示"已连上"，那就是个假指示灯。
		//
		// 🩸 这里的 up 表示「**能不能玩**」，不是「游戏连进来没有」（2026-10-18）：
		// 隧道一通（StateTunnel）就已经能玩了，游戏连入只是"正在玩"的附加信息。
		// 原来只认 StateBridging，于是玩家进房后那二十几秒一直显示没连上 ✗
		//
		// 📌 连阶段一起推过去：界面要分清「正在连」和「没连上」——
		// 那是完全不同的两件事，合并成一句会让新玩家以为出故障 ✗
		b.emit("server", map[string]any{
			"up":    s == node.StateTunnel || s == node.StateBridging,
			"stage": string(s),
		})
		if !first && (s == node.StateTunnel || s == node.StateBridging) {
			upOnce.Do(func() { b.emit("reconnected", "") })
		}
	}
	r.OnNotice = func(msg string) { b.emit("notice", msg) }
	r.OnMembers = func(ms []node.Member) {
		// 📌 收到第一份成员名单 = 服务器**收下我们了**（密码错根本走不到这儿）⇒
		//    这时才推 local 让页面进房间、才响提示音 ✓
		//    重连成功也会再来一份名单 —— ses.hello() 保证这些只发生一次 ✗
		if ses.hello() {
			b.emit("local", map[string]string{"addr": ses.local})
			// 玩家侧：**成功进房**就响这一声。
			// （原来挂在"游戏连入"那个状态上，会被 MC 的两条 TCP 触发两次。）
			if currentSettings().SoundOnConnect {
				playBeep()
			}
		}
		out := make([]map[string]any, 0, len(ms))
		for _, m := range ms {
			out = append(out, map[string]any{
				"id": m.ID, "label": m.Label, "seat": m.Seat, "host": m.Host, "avatar": m.Avatar,
			})
		}
		b.emit("members", out)
	}
	return r
}

// giveUp 重连次数用完（或超时）—— 留在房间，让用户导出日志 / 手动再试。
func (b *backend) giveUp(ses *joinSession, err error, tried int) {
	b.emit("log", fmt.Sprintf("自动重连放弃（试了 %d 次）：%v", tried, err))
	b.setState("连接断了，重连没成功")
	b.markServerFailed()
	b.emit("reconnectfail", fmt.Sprintf(
		"自动重连试了 %d 次都没成功。\n"+
			"先检查网络（Wi-Fi / 代理 / VPN）再点下面「重新连接」。\n"+
			"要留证据的话现在就导出日志 —— 退出房间之后这份就没了。", tried))
	b.endSessionThis(ses, true) // 留在房间
}

// markServerFailed 把玩家侧那盏灯打成「没连上房主」。
//
// 🩸 非打不可（2026-10-18）：不打洞成功、也不推 stopped 的那几条路
// （打洞失败 / 重连放弃）里，灯会**一直停在「正在连接房主…」**——
// 跟旁边的红色横幅自相矛盾，用户会以为软件还在后台拼命试 ✗
func (b *backend) markServerFailed() {
	b.emit("server", map[string]any{"up": false, "stage": "failed"})
}

// fatalMsg 把"会直接退出的错误"翻成给用户看的话。
//
// 🩸 有一条必须改写（2026-10-18）：**重连过程中**发现房间不存在。
//
//	房员和房主往往是同一个网络问题一起断的 —— 房员这边还在退避重连，
//	房主那边房间已经散了 ⇒ 服务器回「房间不存在，检查一下房间 ID」✗
//	照原文案弹出去，他会去反复核对那串**根本没输错**的房间号 ✓
//
//	第一次就连不上（attempt==0）则不改写：那时候真的可能是房间号打错了 ✓
func fatalMsg(err error, attempt int) string {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if attempt > 0 && strings.Contains(msg, "房间不存在") {
		return "房间已经结束了 —— 多半是房主那边也断线了。让房主重新开一个房间再进。"
	}
	return msg
}

// breakReason 把 Go 的原始错误翻成一句人话（横幅、日志、状态栏都用它）。
func breakReason(err error) string {
	if err == nil {
		return "连接断了"
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "穿透服务器"), strings.Contains(s, "服务器"):
		return "和穿透服务器的连接断了"
	case strings.Contains(s, "房主"):
		return "和房主的连接断了"
	default:
		return "连接断了"
	}
}

// endSessionThis 只在自己还是"当前会话"时收尾。
//
// 🩸 为什么需要：上一轮的循环被 cancel 之后，可能比新会话晚一步才走到收尾 ——
// 那时它一 finish 就会把新会话的 busy/run 清掉，界面看着像"刚进去又退出来了" ✗
func (b *backend) endSessionThis(ses *joinSession, quiet bool) {
	b.mu.Lock()
	current := b.joinSes == ses
	b.mu.Unlock()
	if !current {
		return
	}
	b.endSession(quiet)
}

/* ==================== 通用 ==================== */

// Stop 由页面调用：中止当前会话。
func (b *backend) Stop() {
	b.mu.Lock()
	h, r, ses := b.hst, b.run, b.joinSes
	b.mu.Unlock()
	// 🩸 先打断"等退避再重试"那一段（最长 20 秒）：
	//    那一刻**没有 Runner 在跑**，r.Stop() 什么也停不下来 ——
	//    不先 cancel 的话，用户点了「退出房间」还得干等，看着就像卡死 ✗
	if ses != nil {
		ses.cancel()
	}
	if h != nil {
		h.Stop()
	}
	if r != nil {
		r.Stop()
	}
}

// CloseRoom 由页面调用：房主关掉房间（里面所有人都会断开）。
//
// 实现上跟 Stop 一样 —— 房主的信令连接一断，服务器就会广播"房主已断开"，
// 其他朋友的隧道跟着一起断。分开命名是为了页面上一眼看出在干嘛。
func (b *backend) CloseRoom() { b.Stop() }

// LeaveRoom 由页面调用：玩家退出房间（只断自己，房间继续）。
//
// 🩸 走 r.Leave() 而不是 r.Stop()：Leave 会先给服务器发一条 leave，
// 房主那边才能显示「退出了房间」而不是「非正常退出」。
func (b *backend) LeaveRoom() {
	b.mu.Lock()
	r := b.run
	b.mu.Unlock()
	if r != nil {
		r.Leave()
		return
	}
	b.Stop()
}

// secondPortOf 把页面传来的"第二路端口"变成数字；任何不对的情况都返回 0（= 不开）。
//
// 🩸 **再过一遍 CheckPort**：前端传什么都不能直接信 ✗
//
//	虽然信令那边也会校验，但本地先挡一次 —— 别把"脏端口"带到隧道里去 ✓
func secondPortOf(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || netutil.CheckPort(n) != "" {
		return 0
	}
	return n
}

// pickFreePortExcept 跟 pickFreePort 一样，但**跳过 skip** 这个端口。
//
// 🩸 为什么需要：第二路的起点（10998）万一被占，pickFreePort 会顺延，
//
//	可能正好顺延到主端口（10999）⇒ 两条监听撞在同一个号上 ✗
func pickFreePortExcept(prefer, skip int) int {
	for p := prefer; p < prefer+100 && p < 65535; p++ {
		if p == skip {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			ln.Close()
			return p
		}
	}
	return prefer
}

// joinParams 是"上一次加入房间"的完整参数。
//
// 📌 存它只是为了「重新连接」按钮：打洞失败留在房间里时，
//
//	点一下就能用同样的房间/密码/端口再试，不用让用户重填一遍 ✓
type joinParams struct {
	code, nick, roomPass string
	avatar               int
	portHint, extraHint  string
	proto, gameName      string
}

// RetryJoin 由页面调用：用上次的参数再连一次。
//
// 🩸 跟 JoinRoom 的区别有两条：
//
//	① **不清日志** —— 用户之所以还留在房间里，就是因为要复制那份日志 ✗
//	② **沿用同一个本地端口** —— 游戏里填的地址不能因为重连就变了 ✗
func (b *backend) RetryJoin() string {
	b.mu.Lock()
	jp := b.lastJoin
	var local, local2 string
	if b.joinSes != nil {
		local, local2 = b.joinSes.local, b.joinSes.local2
	}
	b.mu.Unlock()
	if jp.code == "" {
		return "没有可以重连的房间，请重新加入"
	}
	// 万一没留下地址（理论上不会）就现挑一个 —— 总比拿空串进 node 强 ✓
	if local == "" {
		local, local2 = pickJoinAddrs(jp)
	}
	return b.beginJoin(jp, local, local2, true)
}
