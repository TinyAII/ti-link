// Package node 实现朋友端：连信令 -> 拿房主候选 -> 打洞 -> KCP -> 本地监听 TCP 供游戏连接。
package node

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"p2plink/internal/game"
	"p2plink/internal/keyex"
	"p2plink/internal/netutil"
	"p2plink/internal/proto"
	"p2plink/internal/punch"
	"p2plink/internal/signal"
	"p2plink/internal/stun"
	"p2plink/internal/tunnel"

	"github.com/xtaci/smux"
)

// State 是给界面显示的阶段。
type State string

const (
	StateStun     State = "stun"
	StateRegister State = "register"
	StateWaiting  State = "waiting"
	StatePunching State = "punching"
	StateTunnel   State = "tunnel"
	StateBridging State = "bridging"
	StateDone     State = "done"
)

// ErrStopped 表示被 Stop() 主动中止。
var ErrStopped = errors.New("已中止")

// PunchError 表示"隧道没打通"—— **可以重试，不该把房间整个关掉**。
//
// 老板 2026-10-18 的要求：打洞失败别把人踢出房间，
// 停在房间页显示「没连上房主」+ 重新连接，这样他还能复制日志 ✗→✓
//
// 📌 用独立类型而不是普通 error：上层要能分清
//
//	"打不通"（可重试、留在房间）和 "密码不对/被踢/服务器断了"（必须退出）✓
type PunchError struct {
	// Msg 是给玩家看的话（punch.Explain 的产物，可以直接放到界面上）
	Msg string
}

func (e *PunchError) Error() string { return e.Msg }

// FatalError 表示**重试也没用**的失败：密码不对、房间没了、被踢、被拉黑、
// 本地端口被占、两边版本对不上。
//
// 🩸 为什么单独分一类（2026-10-18 做「掉线自动重连」时加的）：
//
//	上层要靠它决定**别自动重连** —— 把「房间密码不对」自动重试 5 遍，
//	用户只会在 50 秒后收到同一句话，白等一场 ✗
//
// 📌 分三类，各有各的去处：
//
//	PunchError ⇒ 留在房间 + 横幅 + 手动重连（打不通可能是网络环境问题，值得再试）
//	FatalError ⇒ 直接报错退出（再试一百遍答案都一样）
//	其它       ⇒ 当作"断了"，自动重连（信令抖动、服务器重启这类）✓
type FatalError struct {
	Msg string
}

func (e *FatalError) Error() string { return e.Msg }

// Member 是名单里的一项（朋友侧的只读副本 —— 服务器推给朋友的那份不带 ID，
// 因为朋友又不能踢人，没必要把别人的设备 ID 摊给他看）。
type Member struct {
	// Label 是昵称（纯名字，没起名就是空串）—— 「房主 / 玩家」那截前缀由界面拼
	Label string
	Seat  int // 第几个玩家：房主 0，朋友从 1 起
	// Avatar 是内置头像库的下标（不是图）
	Avatar int
	Host   bool // 是不是房主本人
	ID     string
	// Game 是这位上报的"我在玩哪款游戏"。🩸 自报字段，只当展示/线索 ✗
	// （空 = 没上报；界面上那种就不显示，别堆噪音）
	Game string
}

// Config 是一次加入会话的全部参数。
type Config struct {
	SignalAddr string
	Room       string
	// Token 是**登录令牌**（账号体系里的会话令牌）。
	//
	// 🩸 它替掉的是原来那个「所有客户端共用、还写死在 exe 里」的访问口令 ——
	// 那个挖出来就能连服务器，等于没有鉴权。现在没登录就是连不上。
	Token string
	// RoomPass 是房间密码：房主设的，加入要填对。
	// 📌 **它不再当隧道加密密钥**（见 proto.Msg 的注释）：密钥靠临时 ECDH 协商，
	// 所以密码设得再弱也不影响机密性 —— 它只管「放不放你进来」。
	RoomPass string
	// Nick 是自己在房主成员列表里显示的名字。
	Nick     string
	Avatar   int
	ClientID string
	Local    string // 本机监听地址，如 127.0.0.1:25565

	// Local2 是**第二路**的本机监听地址（空 = 不开）。
	//
	// 🩸 目前只有《饥荒》用：主世界 10999 + 洞穴 10998。
	//    玩家下虫洞时客户端会断开 10999 改连 10998 ⇒ 这边不监听就掉线 ✗
	//    📌 第二路**只开 UDP**（饥荒是纯 UDP，没有 TCP 那半）✓
	Local2 string
	// Game 是**本端上报的游戏名**（取自客户端那份游戏清单）。
	//
	// 用途：后台看板展示 / 按游戏筛选；以及同房两边报的名字不一样时提示"可能连错游戏" ✓
	//
	// 🩸 它是**自报的**，改客户端就能改 —— 只当展示，**不是防线** ✗
	//   （"互相校验游戏名来证明对方没被改过"这件事做不到：判断方和被判断方都在用户手里）
	//   详见 proto.Msg.Game 那段注释 ✓
	// 空 = 不报（界面上显示"未上报"）✓
	Game string

	// GamePort 是本机游戏那个端口（TCP 和 UDP 共用一个号，端口空间是独立的不冲突）。
	// 0 表示"从 Local 里推" —— 一般不用自己填 ✓
	GamePort int
	// EnableUDP 决定要不要**同时**在同一个端口上监听 UDP。
	//
	// 📌 默认关：只用 TCP 的游戏（我的世界 Java、泰拉瑞亚）不需要，
	// 白开一路是浪费 ✓ 等界面接上"这个游戏用什么协议"之后由它来开 ✓
	// ⚠️ 别跟 UDPPort 搞混 —— UDPPort 是**打洞用的本地端口**，跟游戏无关 ✓
	EnableUDP    bool
	StunServers  string
	UDPPort      int
	PunchTimeout time.Duration
	WaitTimeout  time.Duration
}

// Runner 驱动一次加入会话。
type Runner struct {
	cfg Config

	OnState func(State, string)
	// OnLog 一次给两个版本：human 是给人看的（界面显示 + 复制），
	// detail 是带打码地址的版本（只在导出、且用户勾选「含地址」时才用）。
	OnLog func(human, detail string)
	// OnLive 报实时链路状态（延迟/丢包/v4-v6）。连上之后每 2 秒一次 ✓
	OnLive    func(tunnel.LiveStat)
	OnNotice  func(string)   // 被踢 / 被拉黑 / 房主断开时给玩家看的提示
	OnMembers func([]Member) // 房间成员名单（房主每次变动都会推一份过来）

	mu   sync.Mutex
	conn *net.UDPConn
	sess net.Conn
	ln   net.Listener
	tcp  net.Conn
	// 给**游戏**用的那路 UDP（EnableUDP 时才有）。
	// ⚠️ 别跟上面的 conn 搞混：conn 是**打洞/隧道**那条 socket ✓
	udpLn   net.PacketConn
	udpPipe *tunnel.UDPPipe

	// 第二路（见 Config.Local2）✓
	udpLn2   net.PacketConn
	udpPipe2 *tunnel.UDPPipe
	// peerSecond = 房主那边报了第二路（来自 TypePeer 的 GamePort2）✓
	peerSecond bool

	// abortErr 记"不是用户主动停、而是出事才结束"的原因（比如信令断了）。
	// 🩸 用户点「退出房间」走 Stop()，那是正常收尾、不该弹错误框；
	// 而信令断了必须说清原因 —— 两件事得分开，所以理由单独存 ✓
	abortErr error
	cl       *signal.Client
	// candCh 收"房主重开端口后发来的新候选"（打洞重协商用，容量 1）✓
	candCh chan []string

	// 临时密钥协商：私钥只在本机，公钥经服务器转给对方。
	// 拿到对方公钥后各自算会话密钥 —— 服务器拿着两把公钥也算不出来。
	kp      *keyex.Keypair
	hostPub []byte

	// streams 当前有几条游戏连接在转发（MC 一下会开两条 TCP）。
	// 归零才算"游戏真的走了" —— 见 streamOpened / streamClosed。
	streams atomic.Int32

	stop chan struct{}
	once sync.Once
}

// New 建一个 Runner，未填的超时/STUN 会补默认值。
func New(cfg Config) *Runner {
	if cfg.PunchTimeout == 0 {
		cfg.PunchTimeout = 25 * time.Second
	}
	if cfg.WaitTimeout == 0 {
		cfg.WaitTimeout = 180 * time.Second
	}
	if cfg.StunServers == "" {
		cfg.StunServers = netutil.DefaultStunServers
	}
	return &Runner{cfg: cfg, stop: make(chan struct{}), candCh: make(chan []string, 1)}
}

// log 记一条普通日志：不含地址，两个版本是同一份。
func (r *Runner) log(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	r.logPair(s, s)
}

// logPair 记一条日志。
//
// human 是给人看的（界面显示 + 复制日志都用它）——
// detail 带地址，只在**导出**且用户勾选「含地址」时才用得上。
//
// 🩸 detail 里的地址必须已经打码（前两段 + 端口）。
// 完整 IP 不允许出现在客户端的任何地方：界面、复制、导出都不行。
func (r *Runner) logPair(human, detail string) {
	if r.OnLog != nil {
		r.OnLog(human, detail)
	}
}

// logDetailOnly 记一条**只进导出日志**的条目（界面不显示）。
//
// 约定：human 传空串 —— 上层据此判断"这条不给用户看，只留在导出里"。
// 用在 UDP 端口 / STUN / 候选地址这些**过程细节**上：排障时有用，界面上太吵。
func (r *Runner) logDetailOnly(format string, a ...any) {
	r.logPair("", fmt.Sprintf(format, a...))
}

func (r *Runner) state(s State, msg string) {
	if r.OnState != nil {
		r.OnState(s, msg)
	}
}

func (r *Runner) notice(msg string) {
	if r.OnNotice != nil && msg != "" {
		r.OnNotice(msg)
	}
}

// pushMembers 把服务器推来的名单转给界面。
func (r *Runner) pushMembers(raw []proto.Member) {
	if r.OnMembers == nil {
		return
	}
	ms := make([]Member, 0, len(raw))
	for _, x := range raw {
		ms = append(ms, Member{ID: x.ID, Label: x.Label, Host: x.Host, Seat: x.Seat,
			Avatar: x.Avatar, Game: x.Game})
	}
	r.OnMembers(ms)
}

func (r *Runner) stopped() bool {
	select {
	case <-r.stop:
		return true
	default:
		return false
	}
}

// Stop 中止会话并关掉所有连接。
func (r *Runner) Stop() {
	r.once.Do(func() { close(r.stop) })
	r.mu.Lock()
	tcp, sess, ln, conn, cl := r.tcp, r.sess, r.ln, r.conn, r.cl
	r.mu.Unlock()
	// 🩸 逐个判空，别塞进一个 []interface{} 里再判 ——
	// `*signal.Client` 是**指针**，nil 指针装进 interface 之后 `c != nil` 是 true，
	// 接着调 Close() 就是空指针解引用 ✗
	// （Start 在拿到 cl 之前就失败时，Stop 就会踩到）
	if tcp != nil {
		tcp.Close()
	}
	if sess != nil {
		sess.Close()
	}
	if ln != nil {
		ln.Close()
	}
	if conn != nil {
		conn.Close()
	}
	if cl != nil {
		cl.Close()
	}
}

// Leave 主动退出房间：先告诉服务器"我是主动走的"，再收摊。
//
// 🩸 顺序和那 200ms 都要紧：两种断开在 TCP 层面长得一模一样，
// leave 是服务器**唯一**能区分主动退出 / 非正常退出的依据。
// 发完立刻 Close 的话，TCP 会把还没送出去的数据一起丢掉、leave 根本到不了 ——
// 功能就等于没做。200ms 是给这条消息留的发送窗口，短到用户感觉不出来。
func (r *Runner) Leave() {
	r.mu.Lock()
	cl := r.cl
	r.mu.Unlock()
	if cl != nil {
		_ = cl.Send(proto.Msg{Type: proto.TypeLeave})
		time.Sleep(200 * time.Millisecond)
	}
	r.Stop()
}

// Start 阻塞运行，直到隧道结束或被 Stop / 被踢。
func (r *Runner) Start() error {
	// 先把本机端口占住：占不到就立刻报错，别浪费后面的打洞。
	ln, err := net.Listen("tcp", r.cfg.Local)
	if err != nil {
		// 📌 FatalError：端口被别人占着，重试一百次也还是被别人占着 ✗
		return &FatalError{Msg: fmt.Sprintf("本机端口 %s 用不了（多半已被游戏或其它程序占着），换一个再试", r.cfg.Local)}
	}
	r.mu.Lock()
	r.ln = ln
	r.mu.Unlock()
	defer ln.Close()

	// 📌 双栈：候选里可能有 v6 地址，写死 udp4 就发不出去（2026-10-18）
	conn, err := netutil.ListenDual(r.cfg.UDPPort)
	if err != nil {
		return fmt.Errorf("创建 UDP socket 失败: %w", err)
	}
	r.mu.Lock()
	r.conn = conn
	r.mu.Unlock()
	// 🩸 别写 `defer conn.Close()`：重协商会把 r.conn 换成新 socket，
	//    defer 捕获的是**旧的那个** ⇒ 新 socket 永远不关、旧的被关两次 ✗
	defer func() {
		r.mu.Lock()
		c := r.conn
		r.mu.Unlock()
		if c != nil {
			c.Close()
		}
	}()

	port := conn.LocalAddr().(*net.UDPAddr).Port
	cands := netutil.LocalCandidates(port)
	r.logDetailOnly("本地 UDP 端口 = %d", port)

	r.state(StateStun, "探测公网地址…")
	for _, srv := range netutil.SplitList(r.cfg.StunServers) {
		if r.stopped() {
			return ErrStopped
		}
		pub, err := stun.Discover(conn, srv, 2)
		if err != nil {
			r.logDetailOnly("STUN %s 无响应", srv)
			continue
		}
		r.logDetailOnly("STUN 探测成功: 公网地址 = %s", netutil.MaskAddr(pub.String()))
		cands = append([]string{pub.String()}, cands...)
		break
	}

	// 生成一次性密钥对。公钥随注册发出去，由服务器转给对方；
	// 私钥任何时候都不离开本机。
	kp, err := keyex.New()
	if err != nil {
		return fmt.Errorf("生成临时密钥失败: %w", err)
	}
	r.kp = kp

	r.state(StateRegister, "连接穿透服务器…")
	cl, err := signal.Dial(r.cfg.SignalAddr, 6*time.Second)
	if err != nil {
		return fmt.Errorf("连接穿透服务器失败: %w", err)
	}
	r.mu.Lock()
	r.cl = cl
	r.mu.Unlock()
	defer cl.Close()

	if err := cl.Send(proto.Msg{
		Type:     proto.TypeRegister,
		Role:     "join",
		Room:     r.cfg.Room,
		Token:    r.cfg.Token,
		RoomPass: r.cfg.RoomPass,
		ClientID: r.cfg.ClientID,
		Label:    r.cfg.Nick,
		Avatar:   r.cfg.Avatar,
		PubKey:   kp.Public(),
		Cands:    cands,
		// 加入方也报自己的游戏：服务器能看出"两边说的不是同一款" ⇒ 界面提示防误连 ✓
		// 🩸 自报字段，只当展示（见 Config.Game 那段）✗
		Game: r.cfg.Game,
	}); err != nil {
		return fmt.Errorf("注册失败: %w", err)
	}

	r.state(StateWaiting, "正在等待房主…")
	var peerCands []string
	deadline := time.Now().Add(r.cfg.WaitTimeout)
	for len(peerCands) == 0 {
		m, err := cl.Recv(time.Until(deadline))
		if err != nil {
			return fmt.Errorf("等待房主超时或穿透服务器断开: %w", err)
		}
		switch m.Type {
		case proto.TypePeer:
			peerCands = m.Peer
			// 房主报了第二路（饥荒洞穴那类）⇒ 记住，建隧道后要多开一路 ✓
			r.peerSecond = m.GamePort2 != 0
			r.logDetailOnly("收到房主候选 = %s%s", netutil.MaskAddrs(peerCands),
				map[bool]string{true: "（房主有第二路）", false: ""}[r.peerSecond])
			// 房主给的候选里一个公网地址都没有 ⇒ 基本注定失败。
			// 提前记一条，省得事后对着"打洞失败"猜 ✗
			if !netutil.HasPublic(peerCands) {
				r.logDetailOnly("⚠ 房主给的候选里没有公网地址（全是内网段）—— " +
					"多半是他那边 STUN 没探到公网地址，这种打不通")
			}
		case proto.TypeHostKey:
			// 房主的公钥。**打洞之前**就必须拿到 —— 会话密钥和探测包口令都靠它派生。
			r.hostPub = m.PubKey
		case proto.TypeMembers:
			r.pushMembers(m.Members)
		case proto.TypeNotice:
			// 纯提示：只显示，**不 return** —— 它不是结束信号 ✗
			// （跟下面那行 TypeBanned/Kicked 那几种不一样，那些是真"被中断了"）
			r.notice(m.Notice)
		case proto.TypeBanned, proto.TypeKicked, proto.TypeHostGone:
			r.notice(m.Notice)
			// 📌 FatalError：被踢 / 被拉黑 / 房主关了房 —— 自动重连只会
			//    被服务器再踢一次，重复弹同一句话 ✗
			return &FatalError{Msg: fmt.Sprintf("已被中断: %s", m.Notice)}
		case proto.TypeError:
			// 服务器给的文案本来就是给玩家看的（比如「房间密码不对」），
			// 直接用它 —— 前面再套一层"信令错误"反而不知道在说什么
			// 📌 FatalError：房间不存在 / 密码不对，重连也是同样结果 ✗
			return &FatalError{Msg: m.Err}
		}
	}

	if r.stopped() {
		return ErrStopped
	}

	// 隧道建立后仍要盯着信令：可能被踢 / 被拉黑 / 房主断开
	go r.watchSignal(cl)

	// 协商会话密钥。这一步不成就别再往下走了 —— 硬着头皮打洞只会
	// "打通了却握不上手"，比直接报错难查得多。
	if len(r.hostPub) == 0 {
		// 📌 FatalError：对面版本旧、压根不会发公钥 ⇒ 自动重连也是对牛弹琴 ✗
		return &FatalError{Msg: "没拿到房主的公钥（对方客户端可能还是旧版本，请都更新）"}
	}
	key, err := r.kp.Shared(r.hostPub)
	if err != nil {
		return &FatalError{Msg: fmt.Sprintf("协商会话密钥失败: %v", err)}
	}

	r.state(StatePunching, "打洞中…")
	// 打洞过程日志（"正在与对方建立连接…"之类）只进导出，界面不显示。
	punchLog := func(_, detail string) { r.logDetailOnly("%s", detail) }
	// 等满 NoticeAfter 就把状态说得更具体一点 —— 界面一直停在"打洞中…"
	// 会让人以为卡死（老板 2026-10-18 提的）✓
	var told time.Duration
	peer, err := punch.PunchEx(conn, peerCands, r.cfg.PunchTimeout, punch.ProbeToken(key), punchLog,
		func(elapsed, total time.Duration) {
			if elapsed < punch.NoticeAfter || told != 0 {
				return
			}
			told = elapsed
			r.state(StatePunching, fmt.Sprintf("打洞中…（已 %d 秒，最多 %d 秒）",
				int(elapsed.Seconds()), int(total.Seconds())))
			r.logDetailOnly("打洞较慢：已 %d 秒，等到 %d 秒为止",
				int(elapsed.Seconds()), int(total.Seconds()))
		},
		func() bool { return r.stopped() })

	// 用户自己退了 / 被中止：这不是"打不通"，按正常停止处理 ✓
	if errors.Is(err, punch.ErrCanceled) {
		return ErrStopped
	}
	if err != nil {
		r.logDetailOnly("第 1 轮打洞失败：%v", err)
		// 🩸 换端口重协商（2026-10-18）：房主那边也在换，两边各报新候选再打一轮 ✓
		conn, peer, err = r.retryPunch(conn, peerCands, key, punchLog)
		if errors.Is(err, punch.ErrCanceled) {
			return ErrStopped
		}
	}
	if err != nil {
		// 🩸 别把 Go 的原始错误甩到弹窗上（`read udp4 0.0.0.0:58782: i/o timeout`）：
		// 用户看不懂，而且 0.0.0.0 是**自己**这边的监听地址，容易误读成"对方是 0.0.0.0" ✗
		// 原始错误只进日志，弹窗给一句能照着做的话 ✓
		r.logDetailOnly("打洞失败（原始错误）：%v", err)
		// 📌 包成 PunchError：上层靠类型区分"可重试"和"必须退出" ✓
		// 📌 连**自己的**候选一起交给 Explain（2026-10-18）：它要判"甲有 IPv6、乙没有"
		//    这种跨族死局，光看对方的地址判不出来 ✗
		return &PunchError{Msg: punch.Explain("对方", peerCands, cands, r.cfg.PunchTimeout).Error()}
	}
	r.logPair("已连接到房主，隧道已打通",
		fmt.Sprintf("打洞成功，对端 = %s", netutil.MaskAddr(peer.String())))

	// 🩸 **先把打洞通道套上分流层，再交给 KCP** ——
	// 这样同一条通道才能既跑 KCP（TCP 游戏）又跑裸 UDP（UDP 游戏）✓
	// 顺序反了的话 UDP 那一路就没地方走了 ✗
	split := tunnel.NewSplitConn(conn)

	sess, err := tunnel.NewSessionWithKey(split, peer, key)
	if err != nil {
		return fmt.Errorf("建立会话失败: %w", err)
	}
	r.mu.Lock()
	r.sess = sess
	r.mu.Unlock()
	defer sess.Close()

	// 实时状态：每 2 秒一个心跳，攒出延迟和丢包（走的是同一条通道，不额外打洞）✓
	stopProbe := tunnel.StartProbe(split, peer, 2*time.Second, r.OnLive, r.stop)
	defer stopProbe()

	// 游戏要用 UDP 的话，在**同一个端口**上再监听一路 UDP
	if r.cfg.EnableUDP {
		uln, pipe, uerr := startGameUDPListen(split, key, peer, false, r.cfg.Local, r.logDetailOnly)
		if uerr != nil {
			// 📌 UDP 起不来**不该拖垮整条隧道** —— TCP 那路还好好的 ✓
			r.logDetailOnly("UDP 那路没起来（不影响 TCP 游戏）: %v", uerr)
		} else {
			r.mu.Lock()
			r.udpLn, r.udpPipe = uln, pipe
			r.mu.Unlock()
			defer pipe.Close()
			go pipe.KeepAlive()
		}
	}

	// 第二路 UDP（饥荒洞穴）。📌 只开 UDP —— 那款游戏没有 TCP 那半 ✓
	// 只有**房主报了第二路**才开 —— 不然白占一个本地端口 ✓
	if r.cfg.Local2 != "" && r.peerSecond {
		uln2, pipe2, uerr := startGameUDPListen(split, key, peer, true, r.cfg.Local2, r.logDetailOnly)
		if uerr != nil {
			// 同样：起不来不拖垮整条隧道 ✓
			r.logDetailOnly("第二路 UDP 没起来（不影响主端口）: %v", uerr)
		} else {
			r.mu.Lock()
			r.udpLn2, r.udpPipe2 = uln2, pipe2
			r.mu.Unlock()
			defer pipe2.Close()
			go pipe2.KeepAlive()
		}
	}

	mux, err := tunnel.MuxClient(sess)
	if err != nil {
		return fmt.Errorf("建立多路复用失败: %w", err)
	}
	defer mux.Close()

	r.state(StateTunnel, "隧道就绪 —— 游戏客户端连上来即可")
	for {
		tc, err := ln.Accept()
		if err != nil {
			if r.stopped() {
				// 用 abortReason() 而不是死写 ErrStopped：
				// 「信令断了」这种要带着原因上去，用户才知道为什么房间没了 ✓
				return r.abortReason()
			}
			return fmt.Errorf("接受连接失败: %w", err)
		}
		go r.serveStream(mux, tc)
	}
}

// serveStream 给一条新进来的游戏连接开一条复用流。
// MC 连一次服务器会开两条 TCP（先查状态、再真进游戏），所以必须循环接。
func (r *Runner) serveStream(mux *smux.Session, tc net.Conn) {
	// 先把这条连接**认一下游戏**（看握手头几十字节）——
	// 认出是**别的**游戏就直接掐掉（那时候返回的是 nil）✓
	tc = r.sniffGame(tc)
	if tc == nil {
		return
	}

	stream, err := mux.OpenStream()
	if err != nil {
		tc.Close()
		r.log("开流失败: %v", err)
		return
	}
	r.streamOpened()
	tunnel.Bridge(tc, stream, r.logDetailOnly)
	r.streamClosed()
}

// peekedConn 把"已经 peek 过"的连接包一层。
//
// 🩸 为什么需要它：识别要**先看**开头那几十字节，而那是游戏自己的握手 ——
// 读走了游戏就再也连不上 ✗ 所以用 bufio 的 Peek（只挪游标不消费），
// 然后用这个包装类型接着转发：Read 走 bufio（它会先把 peek 过的字节吐出来），
// 其它方法（Write/Close/Deadline…）一律直接交给底层 ✓
type peekedConn struct {
	net.Conn
	br *bufio.Reader
}

func (p *peekedConn) Read(b []byte) (int, error) { return p.br.Read(b) }

// sniffTimeout 是"等握手"的最长时间。
//
// 📌 不能无限等：有的协议是**服务端先开口**，客户端连上之后一声不吭 ⇒
// 死等就把这条连接卡住了 ✗ 等不到就当认不出（放行）✓
const sniffTimeout = 300 * time.Millisecond

// sniffGame 偷看一下连接开头，认出是哪款游戏；**不动数据** ✓
//
// 老板 2026-10-16 晚：「不一样的房间不可加入，服务器端、客户端都要防一下」
// ⇒ 认出**别的**游戏就**把这条连接掐掉**（不是只提示）✓
//
// 🩸 这道拦跟服务端那道**管的事不一样**，别混：
//
//	· 服务端看的是**自报的游戏名** ⇒ 挡「手滑选错游戏」（那东西能伪造 ✗）
//	· 这道看的是**握手数据本身** ⇒ 挡「自报蒙对了、实际在开别的游戏」✓
//	  也就是服务端放行过的那些，由这儿兜 ✓
//
// ⚠️ 但它**也不是防线**：照真游戏的握手伪造一份就能绕 ✗
//
//	它的价值在于「**基于事实**」—— 换启动器、改 exe 名字都不影响它 ✓
//
// 判定一律「**认出别的才动**」：
//
//	· 认不出（我们没资料 / 数据不够）⇒ 放行 ✓
//	· 自己没选游戏（老客户端）⇒ 不比、放行 ✓
//	· 认出的就是自己那款 ⇒ 放行 ✓
//
// 返回 nil 表示「这条连接该掐掉」（认出别的游戏了，**已经 close 过**）✗
// 反证方法：把最后那句 `out.Close(); return nil` 改成 `return out` ⇒
// TestSniffGameDropsMismatch 立刻红 ✓
func (r *Runner) sniffGame(tc net.Conn) net.Conn {
	br := bufio.NewReaderSize(tc, 512)
	out := &peekedConn{Conn: tc, br: br}

	_ = tc.SetReadDeadline(time.Now().Add(sniffTimeout))
	b, err := br.Peek(64)
	_ = tc.SetReadDeadline(time.Time{}) // 清掉，别影响后面的正常转发
	if err != nil && len(b) == 0 {
		return out // 一点数据都没等到 ⇒ 认不出 ⇒ 放行 ✓
	}

	got := game.Sniff(b)
	mine := r.cfg.Game
	if got == "" || mine == "" || got == mine {
		return out
	}
	// 认出是**别的**游戏 ⇒ 掐掉。
	// 📌 先把话说出去再断 —— 反过来的话那句话会被 RST 冲掉（关房间时踩过）✓
	r.notice("连进来的好像是「" + got + "」，但这个房间开的是「" + mine +
		"」—— 已经把这条连接断掉了。切到对应的游戏再连。")
	out.Close()
	return nil
}

// streamOpened / streamClosed 管「现在有几条游戏连接在转发」。
//
// 🩸 为什么要**计数**、而不是"某条结束就切回去"：
// MC 连一次会开**两条** TCP（先查状态、再真进游戏），关掉游戏时它们**不是同一时刻**结束的。
// 按单条判断的话，界面会从「已连入」闪回「隧道就绪」、再闪回来 —— 灯在抖 ✗。
// **计数归零**才是"游戏真的走了"。
//
// 这条状态还直接决定玩家侧的「已连接房主服务器」指示灯（backend.go 拿
// StateBridging 当 up 用）。原来结束时压根不切回去，于是游戏都关了灯还绿着 ✗。
func (r *Runner) streamOpened() {
	if r.streams.Add(1) == 1 {
		r.log("游戏已连入，数据转发中")
		r.state(StateBridging, "游戏已连入，数据转发中")
	}
}

func (r *Runner) streamClosed() {
	if r.streams.Add(-1) <= 0 {
		r.log("游戏已断开，隧道还留着 —— 再进游戏可以直接连")
		r.state(StateTunnel, "隧道就绪 —— 游戏客户端连上来即可")
	}
}

// abortReason 是"这次为什么结束"。
// 用户主动停的返回 ErrStopped（上层当正常收尾，不弹错误框）✓
func (r *Runner) abortReason() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.abortErr != nil {
		return r.abortErr
	}
	return ErrStopped
}

// onSignalLost 处理「和穿透服务器的连接断了」。
//
// 🩸🩸 2026-10-17 事故：这里原来是**静默 return** ✗
//
//	隧道是 P2P 的、本地 25565 监听也还开着 ⇒ 信令断了以后客户端毫无察觉：
//	界面一直显示「已连入、数据转发中」、游戏也能连上本地端口，
//	但房主那边早就没了 ⇒ **僵尸房间**，用户只能干瞪眼 ✗
//	而且房主关房时服务器发的 TypeHostGone 也永远收不到了（信令都断了）——
//	没人会再通知他 ⇒ 只能自己判死。
//
// 抽成单独的函数是为了**能测**：全流程跑一遍要造一整套假服务器
// （TLS / 注册 / 打洞 / 多路复用），那已经超出这段逻辑该操心的范围 ✓
func (r *Runner) onSignalLost(err error) {
	if r.stopped() {
		return // 自己点停的，正常收尾，别把它记成"出事了"
	}
	r.log("与穿透服务器的连接断开：%v", err)
	if r.OnNotice != nil {
		// 🩸 这里**只说事实**，别断言"房间已经结束"（2026-10-18）：
		//    上层现在会试着自动重连 —— 它要是连回来了，这句"已经结束"就是假话 ✗
		//    房间到底结没结束由上层（backend / CLI）决定并另外通知 ✓
		r.OnNotice("和穿透服务器的连接断了。")
	}
	r.mu.Lock()
	if r.abortErr == nil {
		// 📌 同样只陈述事实：结不结束是上层的事（它会重连）✓
		r.abortErr = errors.New("和穿透服务器的连接断了")
	}
	r.mu.Unlock()
	r.Stop()
}

// watchSignal 处理隧道期间的房主指令。
func (r *Runner) watchSignal(cl *signal.Client) {
	for {
		m, err := cl.Recv(0)
		if err != nil {
			// 🩸 绝对不能只 `return`（见 onSignalLost 的注释）
			r.onSignalLost(err)
			return
		}
		switch m.Type {
		case proto.TypeHostKey:
			// 房主的公钥。**打洞之前**就必须拿到 —— 会话密钥和探测包口令都靠它派生。
			r.hostPub = m.PubKey
		case proto.TypePeer:
			// 房主换了本地端口（重协商）会再发一次候选 —— 收下，打洞那边在等 ✓
			r.pushPeerCands(m.Peer)
		case proto.TypeMembers:
			r.pushMembers(m.Members)
		case proto.TypeKicked, proto.TypeBanned, proto.TypeHostGone:
			n := m.Notice
			if n == "" {
				n = "连接已断开"
			}
			r.log("穿透服务器通知：%s", n)
			r.notice(n)
			r.Stop()
			return
		}
	}
}

// pushPeerCands 收下房主的新候选（只留最新的那一份）。
//
// 🩸 容量 1 + 满了覆盖：房主要是连着报两次，只有**最后一次**的地址是对的，
//
//	留着旧的那份反而会把打洞引到废地址上 ✗
func (r *Runner) pushPeerCands(cands []string) {
	if r.candCh == nil || len(cands) == 0 {
		return
	}
	select {
	case r.candCh <- cands:
	default:
		select {
		case <-r.candCh:
		default:
		}
		select {
		case r.candCh <- cands:
		default:
		}
	}
	r.logDetailOnly("收到房主重发的新候选 %s", netutil.MaskAddrs(cands))
}

// retryPunch 换一个本地端口重新协商候选，再打一轮。
//
// 顺序很要紧：**先把自己的新候选报上去，再等房主的** ——
// 反过来的话两边互相干等，谁都不知道对方换到哪个端口了 ✗
//
// 返回新的 socket：后面建隧道要用它，旧的那个已经关了 ✓
func (r *Runner) retryPunch(conn *net.UDPConn, peerCands []string, key []byte,
	punchLog func(human, detail string)) (*net.UDPConn, *net.UDPAddr, error) {

	// ① 换端口（用 0 让系统分配，保证和上一轮不是同一个）
	nc, err := netutil.ListenDual(0)
	if err != nil {
		return conn, nil, fmt.Errorf("重试时开新端口失败: %w", err)
	}
	r.mu.Lock()
	old := r.conn
	r.conn = nc
	r.mu.Unlock()
	if old != nil && old != nc {
		old.Close()
	}

	port := nc.LocalAddr().(*net.UDPAddr).Port
	cands := netutil.LocalCandidates(port)
	for _, srv := range netutil.SplitList(r.cfg.StunServers) {
		pub, err := stun.Discover(nc, srv, 2)
		if err != nil {
			continue
		}
		cands = append([]string{pub.String()}, cands...)
		break
	}
	r.logDetailOnly("重协商：换到本地端口 %d，新候选 %s", port, netutil.MaskAddrs(cands))

	// ② 先发自己的（房主靠它认领我们的新端口）
	r.mu.Lock()
	cl := r.cl
	r.mu.Unlock()
	if cl != nil {
		if err := cl.Send(proto.Msg{Type: proto.TypeReCand, Cands: cands}); err != nil {
			r.logDetailOnly("重协商：新候选没报上去（%v），这一轮大概率白打", err)
		}
	}

	// ③ 再等房主的新候选；等不到就用旧的碰碰运气
	select {
	case fresh := <-r.candCh:
		peerCands = fresh
	case <-time.After(reCandWait):
		r.logDetailOnly("重协商：没等到房主的新候选，用旧地址再打一轮")
	}
	if r.stopped() {
		return nc, nil, punch.ErrCanceled
	}

	// ④ 第二轮
	peer, err := punch.PunchEx(nc, peerCands, r.cfg.PunchTimeout, punch.ProbeToken(key), punchLog,
		nil, func() bool { return r.stopped() })
	if err != nil {
		return nc, nil, err
	}
	r.logDetailOnly("重协商后打通了（%s）", netutil.MaskAddr(peer.String()))
	return nc, peer, nil
}

// reCandWait 等房主新候选的上限（跟 host 那边同一个道理）。
const reCandWait = 4 * time.Second
