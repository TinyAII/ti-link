// IPv6 自检：本机能不能用 IPv6 直连。
//
// 为什么要单独一个按钮（2026-10-18）：
//
//	IPv6 这条路**依赖用户自己家里的网络**：网卡协议开关、光猫授不授权前缀、
//	路由器发不发 RA —— 任何一环没开，客户端就是拿不到地址。
//	而用户从界面上**完全看不出**卡在哪一环，只会觉得"这软件连不上" ✗
//	⇒ 点一下，直接告诉他卡在第几层、该去动什么 ✓
//
// 📌 判定不依赖我们的服务器，纯本机行为（有些机器在断网环境也能跑）✓
package main

import (
	"net"
	"strings"
	"time"

	"p2plink/internal/netutil"
)

// ipv6Probe 一个"能不能出网"的探测目标。挑的都是 v6 上长期稳定的公共地址。
//
// 🩸 端口选 53/80 是因为大多数网络都放行；别用奇怪的端口，否则
//
//	"连不上"可能只是端口被封，会误判成"没有 v6" ✗
var ipv6Targets = []struct{ host, port string }{
	{"2400:3200::1", "53"},         // 阿里公共 DNS（国内可达）
	{"2606:4700:4700::1111", "80"}, // Cloudflare
	{"2001:4860:4860::8888", "53"}, // Google DNS（兜底）
}

// judgeIPv6 把探测结果翻译成结论。
//
// 四档，顺序不能乱 —— 越靠前说明问题越基础：
//
//	off        连链路本地都没有 ⇒ 系统里 IPv6 协议被关了
//	no-address 协议开着但没公网地址 ⇒ 光猫/路由器没往下发前缀
//	no-route   有地址但出不去 ⇒ 路由或防火墙拦了
//	ok         既能拿到地址、又能出去 ⇒ 可以走 v6 直连
//
// 抽成纯函数是为了能单测：真跑一次要好几秒、还得看网络脸色 ✗
func judgeIPv6(global []string, hasLinkLocal, outOK bool) (verdict, headline, detail string) {
	if !hasLinkLocal && len(global) == 0 {
		return "off",
			"系统里的 IPv6 被关掉了（不影响常规联机）",
			"这台电脑连 IPv6 协议都没开 —— 网卡属性里「Internet 协议版本 6 (TCP/IPv6)」没勾上，" +
				"或者被某些「网络优化」软件关掉了。" +
				"想用的话勾上再重启网络。" +
				"平时联机走 IPv4，不受影响；但有一种情况会真的连不上 —— " +
				"对方只有 IPv6、而你这台没有，你俩就没有共同协议了（见下面「没有公网 IPv6」那条）。"
	}
	if len(global) == 0 {
		return "no-address",
			"没有公网 IPv6（不影响常规联机）",
			"这台电脑只有链路本地地址（fe80:: 那种），说明上游没往下发地址 —— " +
				"通常是路由器/光猫的 IPv6 没开，或者运营商没下发前缀（PD）。" +
				"这种情况换哪台电脑都一样，得去动路由器。" +
				"平时联机走 IPv4 打洞，没有 IPv6 照样能连；" +
				"🩸 **但有一种情况会让你彻底连不上**：对方只有 IPv6（比如他的 IPv4 是对称型 NAT、" +
				"打洞必然不通），那他的唯一活路就是 IPv6，而你这边接不住 —— " +
				"这时候你俩之间没有任何能用的协议。" +
				"遇到那种情况，先用手机开热点、电脑连热点，回来看这一项有没有变绿。"
	}
	if !outOK {
		return "no-route",
			"有 IPv6 地址但出不去（不影响常规联机）",
			"拿到了公网地址（" + strings.Join(global, "、") + "），但连不上外面的 v6 地址。" +
				"可能是路由器有 IPv6 但没配默认路由，或者防火墙拦了出站。" +
				"平时走 IPv4 不受影响；只有碰上「对方只有 IPv6」的朋友时，" +
				"这条不通才会真的把你们挡开。"
	}
	return "ok",
		"可以用 IPv6 直连（加分项）",
		"拿到了公网 IPv6 地址，而且能连出去。你和同样有 IPv6 的朋友之间不需要打洞 —— " +
			"直接连就行，也不受对称型 NAT 影响。" +
			"对方没有 IPv6 也不要紧，那时会自动退回 IPv4 打洞。"
}

// hasLinkLocalV6 判断本机有没有链路本地 v6 地址。
//
// 📌 它的意义是"协议开没开"：只要 IPv6 协议启用，系统**一定**会给自己
//
//	配一个 fe80:: 地址（哪怕上游什么都没给）✓ 所以它是最灵敏的探针。
func hasLinkLocalV6() bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.IsLinkLocalUnicast() && ipnet.IP.To4() == nil {
				return true
			}
		}
	}
	return false
}

// globalV6Addrs 本机的公网 v6 地址（去重、跳过 ULA/链路本地）。
func globalV6Addrs() []string {
	var out []string
	seen := map[string]bool{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || !netutil.IsGlobalV6(ipnet.IP) {
				continue
			}
			s := ipnet.IP.String()
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// canReachV6 试着连一个外部 v6 地址，能连上就算出得去。
func canReachV6() (bool, string) {
	for _, t := range ipv6Targets {
		// 📌 必须 JoinHostPort：v6 地址要方括号，手拼 "["+addr+"]" 会把端口塞进去 ✗
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(t.host, t.port), 3*time.Second)
		if err == nil {
			conn.Close()
			return true, net.JoinHostPort(t.host, t.port)
		}
	}
	return false, ""
}

// IPv6Check 由页面调用：跑一次 IPv6 自检。
func (b *backend) IPv6Check() map[string]any {
	global := globalV6Addrs()
	link := hasLinkLocalV6()

	outOK, target := false, ""
	// 没地址就不用白等三次超时了
	if len(global) > 0 {
		outOK, target = canReachV6()
	}

	verdict, headline, detail := judgeIPv6(global, link, outOK)
	return map[string]any{
		"verdict":  verdict,
		"headline": headline,
		"detail":   detail,
		"addrs":    global,
		"link":     link,
		"outOK":    outOK,
		"target":   target,
	}
}
