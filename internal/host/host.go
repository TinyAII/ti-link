// Package host 实现房主端：一条信令连接，为每位朋友各开一条独立隧道。
package host

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"p2plink/internal/keyex"
	"p2plink/internal/netutil"
	"p2plink/internal/proto"
	"p2plink/internal/punch"
	"p2plink/internal/signal"
	"p2plink/internal/stun"
	"p2plink/internal/tunnel"

	"github.com/xtaci/smux"
)

// Config 是房主端的全部参数。
type Config struct {
	SignalAddr string
	Room       string
	// Token 是**登录令牌**（账号体系里的会话令牌）。
	//
	// 🩸 它替掉的是原来那个「所有客户端共用、还写死在 exe 里」的访问口令 ——
	// 那个挖出来就能连服务器，等于没有鉴权。现在没登录就是连不上。
	Token string
	// RoomPass 是房间密码：朋友加入要填对。
	//
	// 📌 它**只管"放不放人进来"**，不再是隧道加密密钥（2026-10-14 起改用临时 ECDH 协商，
	// 见 internal/keyex）。所以密码设得再弱也不影响机密性；
	// 留空也不是"不设防"—— 密钥照样是协商出来的，只是谁都能进房间而已。
	RoomPass string
	// Nick 是房主在成员列表里显示的名字。
	Nick     string
	Avatar   int
	ClientID string
	MaxSize  int
	Local    string // 本机 MC 服务地址，如 127.0.0.1:25565
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

	// GamePort2 是**第二路**游戏端口，0 = 没有。
	//
	// 🩸 目前只有《饥荒》用：主世界 10999 + 洞穴 10998。
	//    玩家下虫洞时客户端会**断开 10999 改连 10998** ⇒ 不开这一路就掉线 ✗
	GamePort2 int
	// EnableUDP 决定要不要**同时**给游戏开一路 UDP。
	//
	// 📌 默认关：不加这个开关的话所有房间都会多跑一路 UDP，
	// 而绝大多数游戏（我的世界 Java、泰拉瑞亚）只用 TCP ⇒ 白费 ✓
	// 等界面接上"这个游戏用什么协议"之后由它来开（见 docs/UDP支持计划.md 阶段 3）。
	EnableUDP    bool
	StunServers  string
	PunchTimeout time.Duration
}

// Member 是名单里的一项。
type Member struct {
	// Label 是昵称（纯名字，没起名就是空串）—— 「房主 / 玩家」那截前缀由界面拼
	Label string
	Seat  int // 第几个玩家：房主 0，朋友从 1 起
	// Avatar 是内置头像库的下标（不是图）—— 两端各自查自己那份
	Avatar int
	Host   bool // 是不是房主本人
	ID     string
	// Game 是这位上报的"我在玩哪款游戏"（界面拿它显示 / 跟自己的比一比）✓
	//
	// 🩸 **客户端自报**，改一下就能填别的 ⇒ 只当展示与"防误连"的线索，
	// **不是**判定依据 ✗（见 proto.Msg.Game 那段）
	// 空 = 没上报（老客户端）✓
	Game string
}

// Ban 是黑名单里的一项（按人合并）。
type Ban struct {
	Key   string // 解除时回传
	Label string // 昵称
	Short string // 设备 ID 前几位，重名时用来区分
}

// Host 驱动房主端。
type Host struct {
	cfg Config

	OnState func(string)
	// OnLog 一次给两个版本：human 是给人看的（界面显示 + 复制），
	// detail 是带打码地址的版本（只在导出、且用户勾选「含地址」时才用）。
	OnLog     func(human, detail string)
	OnMembers func([]Member)
	OnBanList func([]Ban)

	mu      sync.Mutex
	cl      *signal.Client
	tunnels map[string]*peerTunnel // joinID -> 隧道
	// okPeers 记「这位朋友的隧道**真的建成过**」（joinID -> true）。
	//
	// 🩸 只服务于一件事（2026-10-18）：他离开时说哪句话。
	//
	//	连上过     ⇒ 「退出了房间」/「非正常退出」—— 那时候这么说才成立
	//	从没连上过 ⇒ **什么都不说**（界面），只在导出日志里留一条
	//
	// 为什么：打洞失败那一刻已经明说了「建立连接失败」，两秒后又补一句
	// 「非正常退出」不会增加任何信息 —— 只会让房主以为"怎么又断一次" ✗
	// （老板 2026-10-18 从自己的日志里看出来的）
	//
	// 📌 别拿 tunnels 里有没有来判断：隧道对象是**打洞之前**就登记进去的，
	//    打洞失败的那些也在里面 ✗
	okPeers  map[string]bool
	roomPass string // 当前房间密码（只管"放不放人进来"，不再当加密密钥）
	// kp 是本会话的临时密钥对。房主只生成**一对**，公钥发给所有朋友；
	// 每位朋友用自己那对跟它协商，所以每个人的会话密钥都不一样。
	kp   *keyex.Keypair
	bans []Ban
	once sync.Once
	// stopped 记「是不是我们自己关的」—— Run 靠它把主动关闭和真断线分开。
	// 用 atomic 是为了让 Run 那边不用抢锁。
	stopped atomic.Bool
}

type peerTunnel struct {
	udp *net.UDPConn
	// candCh 收"对端重开端口后发来的新候选"（打洞重协商用，容量 1 即可）✓
	candCh chan []string
	sess   net.Conn
	// udpPipe 是「游戏 UDP」那一路（只在 EnableUDP 时才有）。
	//
	// ⚠️ 别跟上面的 udp 搞混：
	//   udp     = **打洞/隧道**那条 socket（KCP 和 UDP 都在它上面跑）
	//   udpPipe = 跑在它上面、专门搬**游戏** UDP 包的那一层
	udpPipe *tunnel.UDPPipe
	// udpPipe2 是第二路（见 Config.GamePort2）✓
	udpPipe2 *tunnel.UDPPipe
}

// New 建一个房主实例。
func New(cfg Config) *Host {
	if cfg.PunchTimeout == 0 {
		cfg.PunchTimeout = 25 * time.Second
	}
	if cfg.StunServers == "" {
		cfg.StunServers = netutil.DefaultStunServers
	}
	return &Host{cfg: cfg, roomPass: cfg.RoomPass,
		tunnels: map[string]*peerTunnel{}, okPeers: map[string]bool{}}
}

// log 记一条普通日志：不含地址，两个版本是同一份。
func (h *Host) log(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	h.logPair(s, s)
}

// logPair 记一条日志。
//
// human 是给人看的（界面显示 + 复制日志都用它）——
// detail 带地址，只在**导出**且用户勾选「含地址」时才用得上。
//
// 🩸 detail 里的地址必须已经打码（前两段 + 端口）。
// 完整 IP 不允许出现在客户端的任何地方：界面、复制、导出都不行。
func (h *Host) logPair(human, detail string) {
	if h.OnLog != nil {
		h.OnLog(human, detail)
	}
}

// logDetailOnly 记一条**只进导出日志**的条目（界面不显示）。
//
// 约定：human 传空串 —— 上层据此判断"这条不给用户看，只留在导出里"。
// 用在打洞 / 牵线 / 建隧道这些**过程细节**上：排障时有用，堆在界面上太吵。
func (h *Host) logDetailOnly(format string, a ...any) {
	h.logPair("", fmt.Sprintf(format, a...))
}

func (h *Host) state(s string) {
	if h.OnState != nil {
		h.OnState(s)
	}
}

// Run 阻塞运行，直到被 Stop 或信令连接断开。
// gamePortOf 从 "127.0.0.1:25565" 里取端口号。取不到返回 0（= 没报）✓
//
// 📌 用 netutil.PortOf —— 全项目取端口**只有那一处**实现 ✓
// 自己再写一遍的话，两边的边界判断迟早不一致 ✗
//
// 📌 报给服务器干什么：让它能拦一下敏感端口（1–1023 / RDP / 数据库那些）✓
//
// 🩸 但服务器本来**看不到**这个端口（它不碰游戏数据）⇒ 只能我们主动报 ✗
// ⇒ 这也意味着**改客户端就能绕** ✗ 那道校验只是"提高门槛" ✓
// 详见 proto.Msg.GamePort 的说明 ✓
func gamePortOf(local string) int {
	p, err := netutil.PortOf(local)
	if err != nil {
		return 0
	}
	return p
}

func (h *Host) Run() error {
	cl, err := signal.Dial(h.cfg.SignalAddr, 6*time.Second)
	if err != nil {
		return fmt.Errorf("连接穿透服务器失败: %w", err)
	}
	h.mu.Lock()
	h.cl = cl
	h.mu.Unlock()
	defer cl.Close()

	// 生成一次性密钥对。房主只生成一对，公钥随注册交给服务器、由它转给每位朋友；
	// 私钥不出本机。每位朋友用自己那对跟它协商，所以每人的会话密钥都不同。
	kp, err := keyex.New()
	if err != nil {
		return fmt.Errorf("生成临时密钥失败: %w", err)
	}
	h.mu.Lock()
	h.kp = kp
	h.mu.Unlock()

	h.state("正在创建房间…")
	if err := cl.Send(proto.Msg{
		Type:     proto.TypeRegister,
		Role:     "host",
		Room:     h.cfg.Room,
		Token:    h.cfg.Token,
		RoomPass: h.cfg.RoomPass,
		ClientID: h.cfg.ClientID,
		Label:    h.cfg.Nick,
		Avatar:   h.cfg.Avatar,
		PubKey:   kp.Public(),
		MaxSize:  h.cfg.MaxSize,
		// 游戏端口也报给服务器，让它能拦一下敏感端口 ✓
		//
		// 🩸 服务器本来看不到这个（它不碰游戏数据），所以只能我们主动报 ✓
		// ⚠️ 这也意味着**改客户端就能绕** —— 所以那道校验只是"提高门槛" ✗
		// 详见 proto.Msg.GamePort 的说明 ✓
		// 解析不出来就报 0（= 没报）⇒ 服务器不校验 ✓
		GamePort: gamePortOf(h.cfg.Local),
		// 第二路（饥荒洞穴）。0 = 没有 ⇒ 服务器不校验、加入方也不开第二路 ✓
		GamePort2: h.cfg.GamePort2,

		// 游戏名一起报上去：后台看板要显示/筛选它 ✓
		// 🩸 同样是**自报**的，只当展示（见 Config.Game 那段的说明）✗
		Game: h.cfg.Game,
	}); err != nil {
		return fmt.Errorf("注册失败: %w", err)
	}

	for {
		m, err := cl.Recv(0)
		if err != nil {
			// 🩸 这里必须分清两件事：
			//   ① 房主自己点了「关闭房间」⇒ 连接是我们主动关的，
			//      Recv 会报 "use of closed network connection" —— 那是**正常收尾**；
			//   ② 网络真断了 ⇒ 才该报错。
			// 原来不分，结果主动关房间的人会看到一个
			// 「穿透服务器连接断开」的错误弹窗（还把 Go 的原始错误原样甩给用户）。
			if h.stopped.Load() {
				return ErrStopped
			}
			// 给弹窗一句人话；Go 的原始错误写进运行日志 ——
			// 用户看不懂那个，但它对排查有用（导出日志时会带上）。
			h.log("与穿透服务器的连接断开：%v", err)
			return errors.New("与穿透服务器的连接断了，检查一下网络再重试")
		}
		switch m.Type {
		case proto.TypeRoomOK:
			h.log("房间 %s 已创建（上限 %d 人）", m.Room, m.MaxSize)
			h.state("房间已创建，等待朋友加入…")
		case proto.TypePair:
			// 日志里要的是「玩家 1」这种能看的名字：朋友没起昵称时，
			// 光有 Label（空串）拼不出来，得带上座位号
			nick := proto.DisplayName(m.Label, m.Seat, false)
			// 界面只说一句「加入了房间」；「正在为 TA 打洞」是过程细节，下沉到导出日志。
			h.logPair("「"+nick+"」加入了房间", "「"+nick+"」加入了，正在为 TA 打洞…")
			go h.newTunnel(m.TargetID, nick, m.Peer, m.PubKey)
		case proto.TypeReCand:
			// 某位朋友换了本地端口（打洞重协商）—— 把新候选交给他那条隧道 ✓
			//
			// 🩸 只塞进通道、不在这里打洞：打洞跑在 newTunnel 那个 goroutine 里，
			//    在这儿插一脚会和它抢同一个 socket ✗
			h.mu.Lock()
			pt := h.tunnels[m.TargetID]
			h.mu.Unlock()
			if pt != nil && pt.candCh != nil {
				// 通道容量 1：满了就把旧的丢掉 —— 只有**最新**的地址才准 ✓
				select {
				case pt.candCh <- m.Peer:
				default:
					select {
					case <-pt.candCh:
					default:
					}
					select {
					case pt.candCh <- m.Peer:
					default:
					}
				}
				h.logDetailOnly("收到「%s」重发的新候选 %s",
					proto.DisplayName(m.Label, m.Seat, false), netutil.MaskAddrs(m.Peer))
			}
		case proto.TypeLeave:
			// 服务器转告：某位朋友走了。Left 说明怎么走的（见 proto.Msg.Left）。
			// 🩸 名字必须从这条消息里取 —— 此刻他早已不在名单里，查不到了。
			nick := proto.DisplayName(m.Label, m.Seat, false)
			human, detail := leaveLine(nick, h.everConnected(m.TargetID), m.Left)
			// 🩸 一律 `log("%s", human)`，别把拼好的句子当格式串 ——
			//    昵称里带个 % 就会把输出搞乱 ✗
			if human == "" {
				h.logDetailOnly("%s", detail) // 界面不显示，只进导出
			} else {
				h.log("%s", human)
			}
			h.mu.Lock()
			delete(h.okPeers, m.TargetID)
			h.mu.Unlock()
		case proto.TypeNotice:
			// 纯提示（比如"进来的这位报的游戏跟你开的不一样"）——
			// 🩸 **别中断**：它不是结束信号，只是顺便说一句 ✗
			// 🩸 用 "%s" 而不是直接传 —— m.Notice 是**拼出来的文案**（里面可能有用户昵称），
			// 直接当格式串的话，昵称里带个 % 就会把输出搞乱（go vet 也会警告）✗
			h.log("%s", m.Notice)
		case proto.TypeMembers:
			ms := make([]Member, 0, len(m.Members))
			for _, x := range m.Members {
				ms = append(ms, Member{ID: x.ID, Label: x.Label, Host: x.Host, Seat: x.Seat,
					Avatar: x.Avatar, Game: x.Game})
			}
			if h.OnMembers != nil {
				h.OnMembers(ms)
			}
		case proto.TypeBanList:
			bs := make([]Ban, 0, len(m.Bans))
			for _, x := range m.Bans {
				bs = append(bs, Ban{Key: x.Key, Label: x.Label, Short: x.Short})
			}
			h.mu.Lock()
			h.bans = bs
			h.mu.Unlock()
			if h.OnBanList != nil {
				h.OnBanList(bs)
			}
		case proto.TypeError:
			// 跟朋友端一样：服务器给的文案本来就是给玩家看的
			//（比如「房间 ID 已被占用，换一个」），再套一层前缀只会把话说糊
			return fmt.Errorf("%s", m.Err)
		}
	}
}

// reCandWait 是重协商时"等对方新候选"的上限。
//
// 📌 两边都在换端口，谁先发谁就先让对方拿到地址 —— 所以 retryPunch 里
//
//	是**先发自己的、再等对方的**。反过来的话两边互相干等，白耗时间 ✗
const reCandWait = 4 * time.Second

// retryPunch 换一个本地端口重新协商候选，然后再打一轮。
//
// 为什么要换端口：有些路由器对某个端口的映射很苛刻（同一个内网端口，
// 换个本地端口可能就分配到宽容的映射）。但**换了端口对方就不知道了** ——
// 所以必须把自己的新候选重新报上去，并且等对端也报新的 ✓
//
// 返回新的 socket —— 调用方后续的 KCP/桥接要用它，不能还用那个已经关掉的旧 socket ✗
func (h *Host) retryPunch(pt *peerTunnel, peerID, label string, peerCands []string, key []byte,
	punchLog func(human, detail string), cl *signal.Client) (*net.UDPConn, *net.UDPAddr, error) {

	// ① 换端口：开新的，关旧的（旧 socket 这会儿已经打完洞失败了）
	udp, err := netutil.ListenDual(0)
	if err != nil {
		return nil, nil, fmt.Errorf("重试时开新端口失败: %w", err)
	}
	h.mu.Lock()
	pt.udp = udp
	h.mu.Unlock()

	port := udp.LocalAddr().(*net.UDPAddr).Port
	cands := netutil.LocalCandidates(port)
	for _, srv := range netutil.SplitList(h.cfg.StunServers) {
		pub, err := stun.Discover(udp, srv, 2)
		if err != nil {
			continue
		}
		cands = append([]string{pub.String()}, cands...)
		break
	}
	h.logDetailOnly("与「%s」重协商：换到本地端口 %d，新候选 %s",
		label, port, netutil.MaskAddrs(cands))

	// ② **先发自己的**新候选（对面要靠它认领我们）
	if cl != nil {
		if err := cl.Send(proto.Msg{Type: proto.TypeHostCand, TargetID: peerID,
			Cands: cands, GamePort2: h.cfg.GamePort2}); err != nil {
			h.logDetailOnly("与「%s」重协商：新候选没报上去（%v），这一轮大概率白打", label, err)
		}
	}

	// ③ 再等对方的新候选；等不到就用旧的（说不定运气好）
	select {
	case fresh := <-pt.candCh:
		peerCands = fresh
		h.logDetailOnly("与「%s」重协商：收到对方的新候选 %s", label, netutil.MaskAddrs(peerCands))
	case <-time.After(reCandWait):
		h.logDetailOnly("与「%s」重协商：没等到对方的新候选，用旧地址再打一轮", label)
	}
	if h.stopped.Load() {
		return nil, nil, punch.ErrCanceled
	}

	// ④ 打第二轮
	peer, err := punch.PunchEx(udp, peerCands, h.cfg.PunchTimeout, punch.ProbeToken(key), punchLog,
		nil,
		func() bool { return h.stopped.Load() })
	if err != nil {
		return udp, nil, err
	}
	h.logDetailOnly("与「%s」重协商后打通了（%s）", label, netutil.MaskAddr(peer.String()))
	return udp, peer, nil
}

// newTunnel 为一位朋友建独立隧道：新开 UDP socket -> 报候选 -> 打洞 -> KCP -> 桥接本机服务。
func (h *Host) newTunnel(peerID, label string, peerCands []string, peerPub []byte) {
	// 先协商会话密钥，不成就别往下走了 —— 硬着头皮打洞只会"打通了却握不上手"
	if h.kp == nil || len(peerPub) == 0 {
		h.logPair(fmt.Sprintf("与「%s」建立连接失败", label),
			fmt.Sprintf("与「%s」协商密钥失败：没拿到对方公钥（版本太旧？）", label))
		return
	}
	key, err := h.kp.Shared(peerPub)
	if err != nil {
		h.logPair(fmt.Sprintf("与「%s」建立连接失败", label),
			fmt.Sprintf("与「%s」协商密钥失败: %v", label, err))
		return
	}

	// 打洞 / 桥接都是**过程细节**：界面不显示，只进导出日志（都带打码地址）。
	punchLog := func(_, detail string) { h.logDetailOnly("%s", detail) }
	bridgeLog := func(format string, a ...any) { h.logDetailOnly(format, a...) }

	// 📌 双栈：同上，v6 候选要发得出去（2026-10-18）
	udp, err := netutil.ListenDual(0)
	if err != nil {
		h.logPair(fmt.Sprintf("与「%s」建立连接失败", label),
			fmt.Sprintf("为「%s」建隧道失败: %v", label, err))
		return
	}
	pt := &peerTunnel{udp: udp, candCh: make(chan []string, 1)}

	h.mu.Lock()
	h.tunnels[peerID] = pt
	cl := h.cl
	h.mu.Unlock()

	defer func() {
		udp.Close()
		h.mu.Lock()
		delete(h.tunnels, peerID)
		h.mu.Unlock()
		h.logDetailOnly("与「%s」的连接已断开", label)
		h.refreshState()
	}()

	port := udp.LocalAddr().(*net.UDPAddr).Port
	cands := netutil.LocalCandidates(port)
	gotPublic := false
	for _, srv := range netutil.SplitList(h.cfg.StunServers) {
		pub, err := stun.Discover(udp, srv, 2)
		if err != nil {
			// 🩸 这里原来是**完全静默**的：三个 STUN 全失败、只把内网地址交给对方，
			// 日志里一个字都没有 ⇒ 事后只能看到对方「打洞失败」，
			// 却查不出"为什么失败" ✗（2026-10-17 排查时卡的就是这个）
			h.logDetailOnly("STUN %s 无响应：%v", srv, err)
			continue
		}
		cands = append([]string{pub.String()}, cands...)
		gotPublic = true
		break
	}
	if !gotPublic {
		h.logDetailOnly("没探到公网地址（STUN 全都没通），只能把内网地址交给对方 —— 这种对面基本连不上")
	}
	h.logDetailOnly("「%s」隧道就绪，候选 %s", label, netutil.MaskAddrs(cands))

	if cl == nil {
		h.logPair(fmt.Sprintf("与「%s」建立连接失败", label), "穿透服务器连接为空，放弃")
		return
	}
	// GamePort2 也带上：让对面知道"这条隧道有几路" ✓
	// 📌 它只是"要不要多开一路"的开关，端口号本身对面用不上（隧道那头是这边连本机游戏）✓
	if err := cl.Send(proto.Msg{Type: proto.TypeHostCand, TargetID: peerID, Cands: cands,
		GamePort2: h.cfg.GamePort2}); err != nil {
		h.logPair(fmt.Sprintf("与「%s」建立连接失败", label),
			fmt.Sprintf("「%s」回报候选失败: %v", label, err))
		return
	}
	// 说清"交给服务器的是什么" —— 只是牵线用的地址，不是游戏数据。
	// 底栏常驻写着"游戏数据不经过服务器"，这句要是含糊说成"交给服务器"，
	// 两句话看着就像在打架。
	h.logDetailOnly("已把「%s」的连接信息交给穿透服务器牵线，等待打通", label)

	// 打洞期间每 0.5 秒回一次进度：等满 NoticeAfter 就把状态改成"还在试"
	// —— 否则界面一直停在"打洞中…"，用户会以为卡死了 ✗
	var told time.Duration
	peer, err := punch.PunchEx(udp, peerCands, h.cfg.PunchTimeout, punch.ProbeToken(key), punchLog,
		func(elapsed, total time.Duration) {
			if elapsed < punch.NoticeAfter || told != 0 {
				return
			}
			told = elapsed
			h.state(fmt.Sprintf("与「%s」打洞中…（已 %d 秒，最多 %d 秒）",
				label, int(elapsed.Seconds()), int(total.Seconds())))
			h.logDetailOnly("与「%s」打洞较慢：已 %d 秒，等到 %d 秒为止",
				label, int(elapsed.Seconds()), int(total.Seconds()))
		},
		func() bool { return h.stopped.Load() })

	if errors.Is(err, punch.ErrCanceled) {
		// 用户自己退了房 / 房间没了 —— 这不是故障，别报"连接失败" ✗
		h.logDetailOnly("与「%s」的打洞已取消（房间已经结束）", label)
		return
	}
	if err != nil {
		h.logDetailOnly("与「%s」第 1 轮打洞失败：%v", label, err)
		// 🩸 换端口再来一轮（重协商，2026-10-18）：
		//    很多路由器对某个端口的映射很苛刻，换个本地端口常常就通了。
		//    但**换了端口对方就不知道**了 ⇒ 必须先等/发新候选，不能闷头重试 ✗
		udp, peer, err = h.retryPunch(pt, peerID, label, peerCands, key, punchLog, cl)
	}
	if err != nil {
		if errors.Is(err, punch.ErrCanceled) {
			h.logDetailOnly("与「%s」的重试已取消（房间已经结束）", label)
			return
		}
		// 原始错误进日志（排查用），界面上给一句人话 ✓
		// 📌 自己的候选一并交给 Explain：它要判"他有 IPv6、我没有"这类跨族死局 ✓
		h.logDetailOnly("与「%s」打洞失败：%v", label, err)
		h.logPair(fmt.Sprintf("与「%s」建立连接失败", label),
			punch.Explain("「"+label+"」", peerCands, cands, h.cfg.PunchTimeout).Error())
		return
	}
	h.logDetailOnly("与「%s」打洞成功（%s）", label, netutil.MaskAddr(peer.String()))

	// 🩸 **先把打洞通道套上分流层，再交给 KCP** ——
	// 这样同一条通道才能既跑 KCP（TCP 游戏）又跑裸 UDP（UDP 游戏）✓
	// 顺序反了的话 UDP 那一路就没地方走了 ✗
	split := tunnel.NewSplitConn(udp)

	sess, err := tunnel.NewSessionWithKey(split, peer, key)
	if err != nil {
		h.logPair(fmt.Sprintf("与「%s」建立连接失败", label),
			fmt.Sprintf("与「%s」建立会话失败: %v", label, err))
		return
	}
	pt.sess = sess
	defer sess.Close()

	// 游戏要用 UDP 的话，给这条隧道再挂一路 UDP（跟 KCP 共用上面那条通道 ✓）
	if h.cfg.EnableUDP {
		pipe, err := startGameUDP(split, key, peer, h.gameUDPAddr(), false, bridgeLog)
		if err != nil {
			// 📌 UDP 起不来**不该拖垮整条隧道** —— TCP 那路还好好的 ✓
			h.logDetailOnly("与「%s」的 UDP 那路没起来（不影响 TCP 游戏）: %v", label, err)
		} else {
			h.mu.Lock()
			pt.udpPipe = pipe
			h.mu.Unlock()
			defer pipe.Close()
			go pipe.KeepAlive()
		}
	}

	// 第二路 UDP（饥荒洞穴）。📌 同样是"起不来不拖垮整条隧道" ✓
	if h.cfg.GamePort2 != 0 {
		if addr2 := h.gameUDPAddrOf(h.cfg.GamePort2); addr2 != nil {
			pipe2, err := startGameUDP(split, key, peer, addr2, true, bridgeLog)
			if err != nil {
				h.logDetailOnly("与「%s」的第二路 UDP 没起来（不影响主端口）: %v", label, err)
			} else {
				h.mu.Lock()
				pt.udpPipe2 = pipe2
				h.mu.Unlock()
				defer pipe2.Close()
				go pipe2.KeepAlive()
			}
		}
	}

	mux, err := tunnel.MuxServer(sess)
	if err != nil {
		h.logPair(fmt.Sprintf("与「%s」建立连接失败", label),
			fmt.Sprintf("与「%s」建立多路复用失败: %v", label, err))
		return
	}
	defer mux.Close()

	h.refreshState()
	h.logDetailOnly("「%s」隧道已建立", label)
	// 🩸 记一笔「这位朋友真连上过」——他断开时决定该说「非正常退出」还是**什么都不说**
	// （见 TypeLeave 那个分支）。📌 不能拿 h.tunnels 里有没有来判断：
	// 隧道对象是**打洞之前**就登记进去的（为了让重协商能找到它）✗
	h.mu.Lock()
	h.okPeers[peerID] = true
	h.mu.Unlock()

	// 每条流 = 游戏的一次 TCP 连接（MC 连一次服务器会开两条：先查状态、再真进游戏）
	for {
		stream, err := mux.AcceptStream()
		if err != nil {
			return
		}
		go func(st *smux.Stream) {
			tcp, err := net.DialTimeout("tcp", h.cfg.Local, 10*time.Second)
			if err != nil {
				h.log("连不上本机的 MC 服务端（%s）—— 确认游戏服务端已经开好、并且监听的就是这个端口", h.cfg.Local)
				st.Close()
				return
			}
			tunnel.Bridge(tcp, st, bridgeLog)
		}(stream)
	}
}

func (h *Host) activeCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, t := range h.tunnels {
		if t.sess != nil {
			n++
		}
	}
	return n
}

// refreshState 把「当前连接人数」刷到界面上。
// 建隧道 / 关隧道 / 踢人之后都要刷，否则界面会留着旧数字。
func (h *Host) refreshState() {
	n := h.activeCount()
	if n == 0 {
		h.state("房间已创建，等待朋友加入…")
		return
	}
	h.state(fmt.Sprintf("已有 %d 位朋友连接", n))
}

// Kick 把某人踢出房间（TA 还能重新加入）。
func (h *Host) Kick(id string) {
	h.sendCmd(proto.TypeKick, id)
	h.closeTunnel(id)
}

// Ban 把某人拉黑（TA 不能再进这个房间）。
func (h *Host) Ban(id string) {
	h.sendCmd(proto.TypeBan, id)
	h.closeTunnel(id)
}

// Unban 把某人移出黑名单。返回错误文案，空 = 成功。
func (h *Host) Unban(key string) error {
	return h.send(proto.Msg{Type: proto.TypeUnban, BanKey: key})
}

// BanList 返回当前黑名单（服务器每次变动都会推一份过来，这里直接给缓存的）。
func (h *Host) BanList() []Ban {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Ban, len(h.bans))
	copy(out, h.bans)
	return out
}

// SetRoomPass 改房间密码。返回错误文案，空 = 成功。
//
// 顺序上**先改本地、再告诉服务器**：反过来的话会出现这样一条缝 ——
// 服务器已经用新密码放新人进来了，房主这边还在拿旧密钥试着握手，
// 那条隧道会莫名其妙打不通，而且报错完全指不到密码上。
func (h *Host) SetRoomPass(pass string) error {
	h.mu.Lock()
	h.roomPass = pass
	h.mu.Unlock()
	return h.send(proto.Msg{Type: proto.TypeSetPass, RoomPass: pass})
}

func (h *Host) sendCmd(t, id string) {
	_ = h.send(proto.Msg{Type: t, TargetID: id})
}

func (h *Host) send(m proto.Msg) error {
	h.mu.Lock()
	cl := h.cl
	h.mu.Unlock()
	if cl == nil {
		return errors.New("房间还没建好")
	}
	return cl.Send(m)
}

// leaveLine 决定"某位朋友离开"时该在界面上说什么。
//
// 🩸 分三种，**别合并**（老板 2026-10-18 从自己的房主日志里看出来的）：
//
//	连上过 + 发了 leave  ⇒ 「退出了房间」      （主动走的）
//	连上过 + 没发 leave  ⇒ 「非正常退出」      （掉线 / 直接关程序）
//	**从没连上过**       ⇒ 界面**一个字都不说**，只进导出日志
//
// 最后那条是重点：「非正常退出」的本义是"**本来是好的**，突然掉了"。
// 对一个打洞就没通过、从头到尾没进过游戏的人说这句，是用错了词 ——
// 而且打洞失败那一刻已经明说过「建立连接失败」了，补这句只会让房主以为
// "刚才连不上，现在又掉了一次"，其实是同一件事 ✗
//
// 返回 human == "" ⇒ 界面不显示，detail 仍然进导出日志（排查要看得到）✓
func leaveLine(nick string, everOK, left bool) (human, detail string) {
	if !everOK {
		return "", "「" + nick + "」一直没连上，离开了房间"
	}
	if left {
		return "「" + nick + "」退出了房间", "「" + nick + "」退出了房间"
	}
	return "「" + nick + "」非正常退出", "「" + nick + "」非正常退出"
}

// everConnected 这位朋友的隧道**建成过**吗？
//
// 📌 判据只有 okPeers 一个来源（见它的注释）—— 别改成查 tunnels ✗
func (h *Host) everConnected(peerID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.okPeers[peerID]
}

func (h *Host) closeTunnel(id string) {
	h.mu.Lock()
	pt := h.tunnels[id]
	delete(h.tunnels, id)
	h.mu.Unlock()
	if pt == nil {
		return
	}
	if pt.sess != nil {
		pt.sess.Close()
	}
	if pt.udp != nil {
		pt.udp.Close()
	}
	h.refreshState()
}

// ErrStopped 表示这次结束是**自己人叫停的**（房主点了关闭房间 / 程序退出），
// 不是故障。调用方据此决定要不要报错 —— 主动关房间不该看到「连接断了」。
//
// 跟 node 侧的 ErrStopped 是一对：朋友退房间走那条，房主关房间走这条。
var ErrStopped = errors.New("房间已关闭")

// Stop 关掉所有隧道与信令连接（服务器随后会广播「房主已断开连接」）。
func (h *Host) Stop() {
	h.once.Do(func() {
		h.stopped.Store(true) // 先标记：免得连接关掉之后 Run 那边来不及判断
		h.mu.Lock()
		cl := h.cl
		ids := make([]string, 0, len(h.tunnels))
		for id := range h.tunnels {
			ids = append(ids, id)
		}
		h.mu.Unlock()

		for _, id := range ids {
			h.closeTunnel(id)
		}
		if cl != nil {
			cl.Close()
		}
	})
}
