// 连接环境自检：能不能连上穿透服务器、有没有代理/VPN 在干扰。
//
// 为什么这两条值得单独测（2026-10-18）：
//
//	① **连不上服务器** —— 用户会以为"软件坏了"。其实多半是本机防火墙、
//	   杀软、或者公司/校园网挡住了 39000 端口。
//	② **代理 / VPN 开着** —— 这类软件会**静默接管 UDP**，打洞必然失败，
//	   而用户根本想不到是自己的加速器。这是最难自查的一类故障 ✗
//
// 📌 两条都不需要服务器配合改动，纯本机行为 ✓
// 📌 只做 Windows（整个 GUI 本来就只有 Windows 版），网卡描述要读 Windows API ✓
package main

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ---------- ① 能不能连上穿透服务器 ----------

// judgeServer 把几次连接的耗时翻译成结论。
//
//	rtts：成功的那些连接的往返毫秒数（失败的次数由调用方单独给）
//
// 📌 阈值取 150ms：这机器在香港，国内家宽到香港一般 30~80ms。
// 超过 150 就是这条线路明显绕，房间操作会发涩 ✓
func judgeServer(rtts []int64) (verdict, headline, detail string) {
	if len(rtts) == 0 {
		return "down",
			"连不上穿透服务器",
			"一个都没连上。最常见的是本机防火墙 / 杀毒软件挡住了这个程序，" +
				"或者你现在用的是公司网、校园网这类对端口有限制的网络。" +
				"先换个网络（比如手机热点）试一下，能连上就说明是这边网络的限制。"
	}

	best := rtts[0]
	for _, v := range rtts[1:] {
		if v < best {
			best = v
		}
	}
	if best >= 150 {
		return "slow",
			fmt.Sprintf("能连上，但延迟偏高（%d ms）", best),
			"连是连得上，就是这条线路绕得远。房间列表、建房这些操作会慢一点；" +
				"游戏本身的速度取决于你和对方之间的直连，跟这个数字关系不大。"
	}
	return "ok",
		fmt.Sprintf("能连上（%d ms）", best),
		"和穿透服务器之间是通的。这个数字只影响房间操作快不快，不影响游戏里的流畅度 —— " +
			"游戏数据是两台电脑直连的。"
}

// serverCheck 连几下服务器，收集成功的往返耗时。
//
// 📌 只做 TCP 连接就立刻断开，**不做 TLS 握手** —— 我们要的是"通不通、多快"，
// 不是内容。握手反而会因为证书和会话多花时间，把 RTT 测偏 ✗
func serverCheck(addr string, tries int) []int64 {
	var out []int64
	for i := 0; i < tries; i++ {
		t0 := time.Now()
		conn, err := net.DialTimeout("tcp", addr, 4*time.Second)
		if err == nil {
			conn.Close()
			out = append(out, time.Since(t0).Milliseconds())
		}
		time.Sleep(120 * time.Millisecond)
	}
	return out
}

// ---------- ② 代理 / VPN ----------

// vpnWords 是"会接管流量"的网卡描述关键词（小写比较）。
//
// 🩸 必须看**网卡描述**而不是名字：Windows 的友好名经常就是「以太网 3」，
//
//	光看名字什么都发现不了 ✗ 描述里才写着 TAP-Windows / WireGuard / Clash 这些 ✓
//
// 📌 虚拟机网卡（VMware / VirtualBox / Hyper-V）**不算** —— 它们不改出口，
//
//	只是多几个候选地址，不是故障原因 ✗
var vpnWords = []string{
	"tap-windows", "wintun", "wireguard", "openvpn", "softether",
	"clash", "v2ray", "xray", "sing-box", "shadowsocks", "trojan",
	"tailscale", "zerotier", "netbird", "hamachi",
	"sangfor", "easyconnect", "深信服", // 企业 VPN
	"uu加速", "迅游", "netease uu", "腾讯网游加速", // 游戏加速器
}

// judgeProxy 把三路证据合成一个结论。
//
//	sysProxy 非空 = 系统代理开着（值形如 "127.0.0.1:7890"）
//	pac 非空      = 配了自动配置脚本（PAC）
//	envProxy 非空 = 环境变量里设了代理
//	vpn           = 命中的虚拟网卡描述
func judgeProxy(sysProxy, pac, envProxy string, vpn []string) (verdict, headline, detail string) {
	if len(vpn) > 0 {
		return "vpn",
			"发现 VPN / 加速器网卡",
			"本机有这些虚拟网卡：" + strings.Join(vpn, "、") + "。" +
				"它们可能把 UDP 流量一起接管了 —— 而打洞靠的就是 UDP，被接管基本打不通。" +
				"如果连不上，先把这类软件完全退出（不是只关界面）再试。"
	}
	if sysProxy != "" || pac != "" || envProxy != "" {
		what := []string{}
		if sysProxy != "" {
			what = append(what, "系统代理 "+sysProxy)
		}
		if pac != "" {
			what = append(what, "自动配置脚本")
		}
		if envProxy != "" {
			what = append(what, "环境变量代理 "+envProxy)
		}
		return "proxy",
			"开着代理",
			"检测到：" + strings.Join(what, "、") + "。" +
				"代理一般只接管浏览器那类 TCP 流量，不一定影响联机；" +
				"但如果连不上，先关掉它试试，可以排除一大类原因。"
	}
	return "ok",
		"没发现代理或 VPN",
		"本机没有系统代理，也没看到会接管流量的虚拟网卡。这一项不用管了。"
}

// systemProxy 读 Windows 的系统代理设置。
func systemProxy() (server, pac string) {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return "", ""
	}
	defer k.Close()

	if v, _, err := k.GetIntegerValue("ProxyEnable"); err == nil && v == 1 {
		if s, _, err := k.GetStringValue("ProxyServer"); err == nil {
			server = s
		}
	}
	if v, _, err := k.GetStringValue("AutoConfigURL"); err == nil {
		pac = v
	}
	return server, pac
}

// envProxy 读环境变量里的代理（很多命令行工具只认这个）。
func envProxy() string {
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY",
		"https_proxy", "http_proxy", "all_proxy"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// vpnAdapters 列出描述里带 VPN/加速器关键词的网卡。
func vpnAdapters() []string {
	size := uint32(15000)
	var buf []byte
	for attempt := 0; attempt < 3; attempt++ {
		buf = make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, 0, 0, first, &size)
		if err == nil {
			var out []string
			for a := first; a != nil; a = a.Next {
				// 🩸 **只看描述会大面积误报**：很多人装过 OpenVPN 或者任一加速器，
				//    网卡就一直躺在「网络连接」里，但从来没启用过 ——
				//    那种情况它不接管任何流量，报出来只会让人白折腾 ✗
				//    ⇒ 必须**已启用 + 有地址**才算数 ✓
				if a.OperStatus != windows.IfOperStatusUp {
					continue
				}
				if a.FirstUnicastAddress == nil {
					continue
				}
				desc := windows.UTF16PtrToString(a.Description)
				low := strings.ToLower(desc)
				for _, w := range vpnWords {
					if strings.Contains(low, w) {
						out = append(out, desc)
						break
					}
				}
			}
			return out
		}
		// 缓冲区不够 ⇒ size 被写成了需要的大小，重来一次
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return nil
		}
	}
	return nil
}

// ---------- 对外的两个绑定 ----------

// ServerCheck 由页面调用：连几下穿透服务器，报通不通、多快。
func (b *backend) ServerCheck() map[string]any {
	rtts := serverCheck(defaultSignal, 3)
	verdict, headline, detail := judgeServer(rtts)
	// 📌 不返回服务器地址：界面上不显示（老板 2026-10-18），
	//    传上去也没人用，留着只会让下次改界面的人以为"该显示它" ✗
	return map[string]any{
		"verdict":  verdict,
		"headline": headline,
		"detail":   detail,
		"rtts":     rtts,
	}
}

// ProxyCheck 由页面调用：查有没有代理 / VPN 在接管流量。
func (b *backend) ProxyCheck() map[string]any {
	sys, pac := systemProxy()
	env := envProxy()
	vpn := vpnAdapters()
	verdict, headline, detail := judgeProxy(sys, pac, env, vpn)
	return map[string]any{
		"verdict":  verdict,
		"headline": headline,
		"detail":   detail,
		"sysProxy": sys,
		"pac":      pac,
		"envProxy": env,
		"vpn":      vpn,
	}
}
