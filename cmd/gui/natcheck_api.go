// 本机网络自检：探自己的公网映射，判断"能不能打洞"。
//
// 为什么要做进软件（2026-10-18）：
//
//	用户报「连不上」，日志里只有一句「打洞失败」—— 那**分不清**是谁的问题：
//	· 自己没公网地址（运营商大内网 / 网络挡 UDP）⇒ 换网络才有用
//	· 对方没有 ⇒ 该让对方换
//	· 两边都有但还是不通 ⇒ 对称型，基本没救
//	三种情况的处理方式完全不同，光看"打洞失败"只能瞎猜 ✗
//	⇒ 让**两边各点一下这个按钮**，各自看到自己的结论，谁的问题一目了然 ✓
//
// 📌 **不经过我们的服务器**：直接问公共 STUN，所以服务器挂了也能用 ✓
// 📌 开**独立**的 UDP socket：不碰正在联机的那条，自检不会打断游戏 ✓
package main

import (
	"net"
	"time"

	"p2plink/internal/netutil"
	"p2plink/internal/stun"
)

// natProbe 是"问一个 STUN，它看到的我是谁"。
type natProbe struct {
	Server string `json:"server"`
	Mapped string `json:"mapped,omitempty"`
	Error  string `json:"error,omitempty"`
}

// judgeNat 根据"每个 STUN 看到的公网 IP:端口"判断 NAT 行为。
//
// 🩸 判据是 NAT 分类的标准做法：**同一个本地端口往不同目标发包，公网端口变不变**。
//
//	不变 ⇒ 端点无关映射（锥型）⇒ 打洞能成 ✓
//	变了 ⇒ 对称型 ⇒ 对方回的包进不来，打洞基本没戏 ✗
//
// 抽成纯函数是为了**能单测** —— 真跑一次要七秒、还得看网络脸色 ✗
func judgeNat(mapped []string) (verdict, headline, detail string) {
	if len(mapped) == 0 {
		return "blocked",
			"探不到公网地址",
			"三个 STUN 服务器都没回应。可能是你的网络挡住了 UDP，也可能是 STUN 暂时不可用。" +
				"换个网络再试一次；如果一直这样，基本可以确定是网络限制。"
	}

	host, port := natHostPort(mapped[0])

	// 拿到的"公网地址"其实不是公网地址 ⇒ 整条宽带在运营商大内网（CGNAT）后面
	if !netutil.IsPublicIP(host) {
		return "cgnat",
			"运营商大内网：没有真正可被访问的公网地址",
			"你拿到的地址是 " + host + "，它不是公网地址 —— 说明整条宽带在运营商的大内网后面。" +
				"这种网络打洞基本不通，换一个网络（换个运营商 / 换热点）才可能好。"
	}

	if len(mapped) == 1 {
		return "unknown",
			"只探到一个结果，判不出来",
			"至少要有两个 STUN 有回应才能比较。可以再点一次自检；如果总是只有一个通，" +
				"多半是网络对 UDP 有限制。"
	}

	same := true
	for _, m := range mapped[1:] {
		if _, p := natHostPort(m); p != port {
			same = false
			break
		}
	}
	if same {
		return "cone",
			"锥型 NAT（最容易打洞的一种）",
			"几个服务器看到的公网端口都是 " + port + "，说明映射是「端点无关」的。" +
				"你的网络适合直连 —— 如果还是连不上，问题多半在对方那边（让他也点一次自检）。"
	}
	return "symmetric",
		"对称型 NAT：别人回的包进不来",
		"不同目标看到的公网端口不一样（" + port + " 等），意味着对方回包的端口和你发出去的源端口对不上。" +
			"这种网络打洞打不通，只能换网络。"
}

// natHostPort 拆 host:port（拆不开就整串当 host）。
func natHostPort(s string) (string, string) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return s, ""
	}
	return h, p
}

// NatCheck 由页面调用：跑一次本机网络自检，返回给界面渲染。
//
// 最坏情况要 7 秒左右（3 个服务器 × 2 次 × 1.2 秒），所以界面那边得先显示"检测中"✓
func (b *backend) NatCheck() map[string]any {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		return map[string]any{"error": "建 UDP socket 失败：" + err.Error()}
	}
	defer conn.Close()

	var probes []natProbe
	var mapped []string
	for _, srv := range netutil.SplitList(netutil.DefaultStunServers) {
		addr, derr := stun.Discover(conn, srv, 2)
		if derr != nil {
			probes = append(probes, natProbe{Server: srv, Error: derr.Error()})
			continue
		}
		probes = append(probes, natProbe{Server: srv, Mapped: addr.String()})
		mapped = append(mapped, addr.String())
		// 稍微错开，别把三个服务器挤在同一瞬间（有的公共 STUN 会限速）
		time.Sleep(120 * time.Millisecond)
	}

	verdict, headline, detail := judgeNat(mapped)
	return map[string]any{
		"localPort": conn.LocalAddr().(*net.UDPAddr).Port,
		"probes":    probes,
		"ok":        len(mapped),
		"verdict":   verdict,
		"headline":  headline,
		"detail":    detail,
	}
}
