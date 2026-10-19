// Package punch 实现 UDP 打洞：同时往多个候选地址发探测包，收到对方**带对口令**的探测包才算打通。
//
// 探测包里带的口令由会话密钥派生 —— 光靠"内容对不对"是不够的，
// 那样任何猜到 UDP 端口的人都能抢走连接。
package punch

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	"p2plink/internal/netutil"
)

// probeMagic 探测包的开头。后面跟着由会话密钥派生的口令。
const probeMagic = "P2PLINK-PROBE-"

// ProbeToken 从会话密钥派生探测包口令。
//
// 🩸 为什么要口令：原来只要发来内容等于 "P2PLINK-PROBE" 的包，就把它当"对方" ——
// 谁抢在前面谁就是对方。有会话密钥之后，抢包的人算不出这个口令
// （它由 ECDH 共享密钥派生，服务器和旁人都不知道），于是连打洞都插不进来。
func ProbeToken(key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("p2plink-probe-v1"))
	return hex.EncodeToString(m.Sum(nil)[:6])
}

// ProbeFor 拼出这一次要发的探测包内容。
func ProbeFor(token string) []byte { return []byte(probeMagic + token) }

// Punch 向 cands 中的所有地址持续发送探测包，直到收到对方**带对口令**的探测包。
// 返回对方真实来源地址（后续通信直接用它）。
//
// token 由会话密钥派生（见 ProbeToken）。口令不对的包一律忽略 ——
// 没这道关，任何猜到 UDP 端口的人都能把连接抢走。
//
// logf 收两个版本：human 是给人看的（界面用），detail 带地址 —— 已经打过码。
// ErrCanceled 表示打洞被主动放弃（用户退出房间 / 房间被关）。
var ErrCanceled = errors.New("打洞已取消")

// errPunchTimeout 是"等够了、对方一直没回应"。
// 📌 不要再把 `read udp4 0.0.0.0:58782: i/o timeout` 甩出去 ——
//
//	那个 0.0.0.0 是**自己**的监听地址，很容易被读成"对方是 0.0.0.0" ✗
var errPunchTimeout = errors.New("打洞超时（对方一直没回应）")

// punchTick 读等待的粒度。
//
// 🩸 为什么不用一个整 timeout 的 ReadDeadline：那样中途既报不了进度、
//
//	也响不了"取消"。切成小段读，每段结束后看一眼时间/取消标志 ✓
const punchTick = 500 * time.Millisecond

// NoticeAfter 是"等多久之后该给用户一个提示"的时长。
//
// 老板 2026-10-18 的原话：「固定 25 秒太长，小白用户等 5 秒就以为软件卡死了」。
// 📌 但不建议缩短**超时本身**：我们真机测过，慢线路 25 秒都没通 ✗
//
//	缩短超时会误杀本来能通的组合 —— 所以只加提示，不砍预算 ✓
const NoticeAfter = 8 * time.Second

// Punch 是最简形态：不看进度、不能中途取消（单测和本机自查用）。
func Punch(conn *net.UDPConn, cands []string, timeout time.Duration, token string,
	logf func(human, detail string)) (*net.UDPAddr, error) {
	return PunchEx(conn, cands, timeout, token, logf, nil, nil)
}

// PunchEx 是带进度和取消的打洞。
//
//	onTick(elapsed, total)  —— 每 punchTick 报一次，用来给界面"还活着"的信号
//	canceled()              —— 返回 true 就立刻收手（用户已经退出/房间没了）
func PunchEx(conn *net.UDPConn, cands []string, timeout time.Duration, token string,
	logf func(human, detail string),
	onTick func(elapsed, total time.Duration),
	canceled func() bool) (*net.UDPAddr, error) {
	addrs := make([]*net.UDPAddr, 0, len(cands))
	for _, c := range cands {
		host, _, err := net.SplitHostPort(c)
		if err != nil || !usableCand(host) {
			continue
		}
		// 📌 用 "udp" 而不是 "udp4"：候选里可能有 v6，
		//    写死 udp4 会让 v6 地址解析直接失败（2026-10-18）
		if a, err := net.ResolveUDPAddr("udp", c); err == nil {
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		return nil, ErrNoValidCandidate
	}

	want := ProbeFor(token)
	done := make(chan struct{})
	defer close(done)

	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				for _, a := range addrs {
					conn.WriteToUDP(want, a)
				}
			}
		}
	}()

	deadline := time.Now().Add(timeout)
	buf := make([]byte, 2048)
	for {
		if canceled != nil && canceled() {
			return nil, ErrCanceled
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			return nil, errPunchTimeout
		}
		wait := punchTick
		if remain < wait {
			wait = remain
		}

		conn.SetReadDeadline(time.Now().Add(wait))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				// 这一段没收到东西是正常的（对方可能还没发）—— 报个进度继续等
				if onTick != nil {
					onTick(timeout-remain, timeout)
				}
				continue
			}
			// 不是超时：多半是 conn 被 Stop() 关掉了
			if canceled != nil && canceled() {
				return nil, ErrCanceled
			}
			return nil, err
		}
		// 口令不对就当没收到：不是对方发的，或者对方版本对不上
		if !bytes.Equal(buf[:n], want) {
			continue
		}
		// 地址打码后再写日志：这条会被显示在界面上、会被截图发出去
		logf("正在与对方建立连接…",
			fmt.Sprintf("收到对方探测包，真实来源 = %s", netutil.MaskAddr(from.String())))
		conn.WriteToUDP(want, from)
		conn.SetReadDeadline(time.Time{})
		return from, nil
	}
}

// ErrNoValidCandidate 表示候选地址列表里没有一个能解析。
var ErrNoValidCandidate = errors.New("punch: 没有可用的候选地址")

// usableCand 判断一个候选能不能拿来打洞。
//
// 🩸 **只收 IPv4 字面量，域名一律拒绝。** 候选项是**对方**给的，
// 而我们拿到之后要 `ResolveUDPAddr` + 往它发包 —— 允许域名就等于
// 让别人指定我们去解析谁：既能拿我们当 DNS 放大器，又能借我们的手去探内网。
// 拒绝域名之后这条路就断了（真机联机时 STUN 给回来的本来就是 IP，不受影响）。
//
// 组播 / 未指定 / 广播 / 保留段也一并拒掉：往那些地址发包要么没意义，要么是帮凶。
// 回环**保留** —— 本机自查（tmp/e2e_pass.py）就是靠它跑通的。
func usableCand(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false // 域名或者乱七八糟的串
	}

	// v4：原来那套判断不动
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4.IsUnspecified(), // 0.0.0.0
			v4.IsMulticast(),        // 224.0.0.0/4
			v4.Equal(net.IPv4bcast): // 255.255.255.255
			return false
		}
		if v4[0] >= 240 { // 240.0.0.0/4 保留段
			return false
		}
		return true
	}

	// v6（2026-10-18 加）：只认**全局**地址。
	// 📌 链路本地（fe80::）和 ULA（fc00::/7）发给对面等于发了个废地址 ✗
	// 📌 回环放行 —— 跟 v4 的 127.0.0.1 一个道理，本机自查要用 ✓
	if ip.IsLoopback() {
		return true
	}
	return netutil.IsGlobalV6(ip)
}

// Explain 把"打洞失败"翻译成用户看得懂、而且知道下一步该干嘛的一句话。
//
// 🩸 原来是直接把 Go 的原始错误甩到弹窗上：
//
//	打洞失败: read udp4 0.0.0.0:58782: i/o timeout
//
// 三个问题：
//  1. 用户看不懂 i/o timeout；
//  2. **0.0.0.0:58782 是自己这边的监听地址**，特别容易被误读成"对方的地址是 0.0.0.0" ✗
//  3. 它把"该怎么办"完全藏起来了 —— 而这恰恰是唯一有价值的信息。
//
// 原始错误不能丢，但它的去处是**运行日志**，不是弹窗 ✓
//
// peer 是"对方"在句子里的说法：访客侧叫"对方"，房主侧是「小明」这种具体名字。
//
// 📌 分两句话的依据是 **对面有没有给出公网地址**：
//
//	· 一个都没有 ⇒ 对面压根没探到自己的公网地址（网络挡了 UDP）
//	  ⇒ 重试一万次也没用，只能换网络 —— 这种话必须说死，别让人反复重试 ✗
//	· 有公网地址 ⇒ 是"这一对网络"打不通，值得换个网络再试 ✓
func Explain(peer string, peerCands, myCands []string, timeout time.Duration) error {
	if !netutil.HasPublic(peerCands) {
		// 这一档是**对方**的问题：他连公网地址都没探到（UDP 被挡了）
		return fmt.Errorf("%s那边没探到公网地址 —— 他的网络把 UDP 挡住了"+
			"（公司网、校园网、运营商大内网都会这样），这种情况直连一定打不通。\n"+
			"让他换个网络（手机热点或另一条宽带）再试；你这边怎么弄都没用。", peer)
	}
	sec := int(timeout.Round(time.Second).Seconds())
	if sec <= 0 {
		sec = 25
	}

	// 🩸 先看是不是"甲有 IPv6、乙没有"（2026-10-18 加）。
	//
	// 这种最该**说准**：两边各有各的公网地址，看着像"都能连"，
	// 实际上一方唯一的出路是 v6、而另一方接不住 ✗
	// 原来这儿只会给一句"换成 IPv6 再试"—— 而软件里根本没有这个开关，
	// 用户照着做一百遍也没用。见 netutil.CrossFamily 的说明 ✓
	peerHas6Only, myHas6Only := netutil.CrossFamily(myCands, peerCands)
	if peerHas6Only {
		return fmt.Errorf("和%s直连没打通（%d 秒没回应）。\n"+
			"他那边**有公网 IPv6，你这边没有** —— 这是他唯一的活路，你接不住。\n"+
			"他的 IPv4 打不通（多半是对称型 NAT：每连一个新地址就换一个端口，猜不到）。\n"+
			"办法只有两个：\n"+
			"1. 你换一个能拿到 IPv6 的网络 —— 先用手机开热点、电脑连热点，"+
			"再到「网络自检」看 IPv6 那项变绿没有。绿了就说明你俩能通\n"+
			"2. 让他换一个能打洞的 IPv4 网络（换条宽带，或者用手机流量）\n"+
			"📌 关 VPN、在设置里翻什么开关都没用 —— 缺的是地址，不是开关。", peer, sec)
	}
	if myHas6Only {
		return fmt.Errorf("和%s直连没打通（%d 秒没回应）。\n"+
			"你这边**有公网 IPv6，他没有** —— 只有你能发给他，他发不回来。\n"+
			"他的 IPv4 打不通（对称型 NAT / UDP 被挡）。\n"+
			"办法：\n"+
			"1. 让他换一个能拿到 IPv6 的网络（手机热点常常有）\n"+
			"2. 或者你俩连**同一个**网络（同一台路由器 / 同一个热点上的两台机器）"+
			"—— 那样走内网地址，必定能通\n"+
			"📌 你这边关 VPN、翻开关都没用 —— 缺的是他那边的地址。", peer, sec)
	}

	// 🩸 别写「请检查网络」这种废话 —— 用户不知道该检查什么 ✗
	//    给三条**按成功率排**的具体动作，最后一条是保底一定能通的 ✓
	return fmt.Errorf("和%s直连没打通（%d 秒没回应）。按顺序试：\n"+
		"1. 双方都把 VPN / 游戏加速器完全退出（关窗口不算，要看托盘里还有没有）\n"+
		"2. 换成 IPv6 再试 —— 先在「网络自检」里看看两边有没有 IPv6\n"+
		"3. 保底办法：让一方开手机热点，两边都连这个热点。"+
		"同一个网络里必定能通（会走手机流量，但联机本身只要几十 kbps）", peer, sec)
}
