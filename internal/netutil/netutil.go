// Package netutil 放几个网络相关的小工具。
package netutil

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// LocalCandidates 列出本机的候选地址（IPv4 + 全局 IPv6），末尾附上回环地址。
//
// 📌 v6 是 2026-10-18 加的：v6 没有 NAT，本机全局地址**就是**对方能连的地址，
//
//	不需要 STUN 去探映射 ✓ 但只收全局地址（链路本地/ULA 发出去没用）✗
func LocalCandidates(port int) []string {
	var v4, v6 []string
	ifaces, err := net.Interfaces()
	if err == nil {
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
				if !ok {
					continue
				}
				if ip4 := ipnet.IP.To4(); ip4 != nil {
					v4 = append(v4, fmt.Sprintf("%s:%d", ip4, port))
					continue
				}
				// 📌 v6 只收**全局**地址 —— 链路本地（fe80::）出了这条链路就没意义，
				//    ULA（fc00::/7）是内网自留段，发给对面也用不上 ✗
				//    格式必须走 JoinHostPort：v6 地址要用方括号包起来（[2409:...]:27154）✓
				if IsGlobalV6(ipnet.IP) {
					v6 = append(v6, net.JoinHostPort(ipnet.IP.String(), strconv.Itoa(port)))
				}
			}
		}
	}

	// 顺序：先 v4 再 v6。
	// 📌 v6 不优先是因为**不能假定对方有**；打洞是并发的，谁先回用谁，
	//    顺序只影响日志好看，不影响结果 ✓
	// 🩸 去重：一台机器多网卡（VMware / Hyper-V / 双网口）时，
	//    同一个地址会出现好几次 —— 白打几轮探测包，还占掉候选条数上限 ✗
	seen := make(map[string]bool, len(v4)+len(v6)+1)
	out := make([]string, 0, len(v4)+len(v6)+1)
	for _, s := range v4 {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range v6 {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	loop := fmt.Sprintf("127.0.0.1:%d", port)
	if !seen[loop] {
		out = append(out, loop)
	}

	// 🩸 卡住条数：服务端 maxCands = 10，调用方还会在最前面插一个公网地址。
	//    装了 VMware / Hyper-V 的机器网卡很多，不掐一下可能超限，
	//    超限是**整条消息被拒**（候选发不出去，打洞压根不开始）✗
	if len(out) > maxLocalCands {
		out = out[:maxLocalCands]
	}
	return out
}

// maxLocalCands 本机候选的条数上限（服务端允许 10，留一个位置给公网地址）。
const maxLocalCands = 8

// ListenDual 开一个"v4 和 v6 都能收发"的 UDP socket。
//
// 为什么需要它（2026-10-18 上 IPv6）：
//
//	打洞原来写死 `udp4`，于是候选里的 v6 地址**根本发不出去** ——
//	收集和判定都做好了，最后一步没接上 ✗
//
// 🩸 **必须带兜底**：有些机器上 IPv6 协议是**禁用**的（我们真见过：
//
//	网卡属性里「Internet 协议版本 6」没勾）—— 那种机器绑 `[::]` 会直接失败。
//	要是没有这条兜底，为了支持 v6 反而把 v4 也弄挂了 ✗
//
// 📌 双栈 socket 收到的 v4 包，来源会显示成 `::ffff:1.2.3.4`。
//
//	我们只在两处用到来源地址：打码写日志（MaskAddr 认得出，会走 v4 分支）
//	和"回包给真实来源"（WriteToUDP 对 `::ffff:` 也照发不误）—— 都不用特判 ✓
func ListenDual(port int) (*net.UDPConn, error) {
	if c, err := net.ListenUDP("udp", &net.UDPAddr{Port: port}); err == nil {
		return c, nil
	}
	return net.ListenUDP("udp4", &net.UDPAddr{Port: port})
}

// IsGlobalV6 判断一个 v6 地址是不是"跨网也能用"的。
//
// 排除这几类：
//
//	· 链路本地 fe80::/10  —— 只在本链路内有效，发给对面等于发了个废地址
//	· 唯一本地 ULA fc00::/7（含 fd00::/8） —— 内网自留段，跨网不通
//	· 回环 / 组播 / 未指定
//	· IPv4 映射地址 ::ffff:a.b.c.d —— 那不是真 v6
func IsGlobalV6(ip net.IP) bool {
	if ip == nil || ip.To4() != nil {
		return false
	}
	if len(ip) != net.IPv6len {
		return false
	}
	if ip.IsLoopback() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return false
	}
	// fc00::/7：第一个字节的高 7 位是 1111110
	if ip[0]&0xfe == 0xfc {
		return false
	}
	return ip.IsGlobalUnicast()
}

// SplitList 把逗号分隔的配置切成非空项（去空格）。
func SplitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

/* ---------- 地址打码 ---------- */

// MaskAddr 把地址打码，只留前两段和端口：
//
//	120.227.55.14:1962   ->  120.227.*.*:1962
//	[2408:8214::1]:1234  ->  2408:8214::*:1234
//
// 为什么要打码而不是直接删掉：P2P 直连意味着日志里必然出现双方的公网地址，
// 而日志是会被截图发出去的（求助、发群）。打码之后既不会泄露完整地址，
// 又看得出"是不是同一个对端" —— 排障时仍然有用。
func MaskAddr(s string) string {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return maskHost(s)
	}
	return net.JoinHostPort(maskHost(host), port)
}

// MaskAddrs 把一组地址打码后拼成一行（给「候选 = [...]」那种日志用）。
func MaskAddrs(addrs []string) string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, MaskAddr(a))
	}
	return strings.Join(out, " ")
}

func maskHost(host string) string {
	ip := net.ParseIP(host)
	if ip == nil {
		// 域名之类，只留最后一段：stun.qq.com -> *.com
		if i := strings.LastIndex(host, "."); i > 0 {
			return "*" + host[i:]
		}
		return "*"
	}
	// 回环地址（127.0.0.1）原样留着 —— 它本来就是这个样子，没有隐私价值，
	// 打码成 127.0.*.* 反而让日志难读
	if ip.IsLoopback() {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.*.*", v4[0], v4[1])
	}
	v6 := ip.To16()
	return fmt.Sprintf("%02x%02x:%02x%02x::*", v6[0], v6[1], v6[2], v6[3])
}

// DefaultStunServers 是可用的公共 STUN 服务器（依次尝试）。
const DefaultStunServers = "stun.miwifi.com:3478,stun.hitv.com:3478,stun.l.google.com:19302"

// PublicAddr 用给定 UDP socket 依次向各 STUN 服务器探测公网地址，返回第一个成功的。
func PublicAddr(conn *net.UDPConn, servers []string, tries int, discover func(*net.UDPConn, string, int) (*net.UDPAddr, error)) *net.UDPAddr {
	for _, srv := range servers {
		addr, err := discover(conn, srv, tries)
		if err == nil {
			return addr
		}
	}
	return nil
}

// IsPublicIP 判断一个地址是不是**真正能从公网直接连到**的地址。
//
// 除了 RFC1918 私网段，这几种也要排除：
//
//	· 回环 / 链路本地 / 未指定 / 组播 —— 对我们没有意义
//	· **运营商大内网 100.64.0.0/10（CGNAT）** —— 关键
//	  Go 的 IsPrivate() 不覆盖这一段，但它的性质和私网一样：
//	  你根本没有一个真正的公网地址，别人打不进来 ✗
//	  （STUN 如果回的是 100.64.x.x，就说明整条宽带都在 CGNAT 后面）
func IsPublicIP(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// 100.64.0.0/10：运营商大内网
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
	}
	return true
}

// PublicFamilies 把一组候选按地址族分开，看**各自有没有公网地址**。
//
// 📌 问的不是"一共有几个地址"，而是"这个族我们能不能直接用" ✓
func PublicFamilies(cands []string) (has4, has6 bool) {
	for _, c := range cands {
		host, _, err := net.SplitHostPort(c)
		if err != nil {
			host = c
		}
		if !IsPublicIP(host) {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			has4 = true
		} else {
			has6 = true
		}
	}
	return has4, has6
}

// CrossFamily 看两边是不是"一个人有 IPv6、另一个人没有"。
//
// 返回：
//
//	peerHas6Only = 对端有公网 IPv6、本端没有
//	myHas6Only   = 本端有公网 IPv6、对端没有
//
// 🩸 只用来**改打洞失败的文案**，不参与任何连接决策 ✗ 成功了当然不提这些。
//
// 为什么值得单独判：两边各有各的公网地址、看着"都能连"，实际上
//
//	他有 IPv6 + 对称型 IPv4 ⇒ 他唯一出路是 v6
//	我没有 IPv6              ⇒ 我接不住他的 v6，而他的 v4 又打不通
//
// 这种情况原来只会给一句"换成 IPv6 再试"，而软件里压根没有这个开关 ——
// 用户照着做一百遍也没用 ✗ 见 punch.Explain 里对应那两段文案。
//
// 📌 只看 v6：v4 是所有人的底线，连 v4 都没有的话
// punch.Explain 开头那条「没探到公网地址」就兜住了 ✓
func CrossFamily(myCands, peerCands []string) (peerHas6Only, myHas6Only bool) {
	_, my6 := PublicFamilies(myCands)
	_, peer6 := PublicFamilies(peerCands)
	return peer6 && !my6, my6 && !peer6
}

// HasPublic 判断候选列表里有没有**至少一个**公网地址。
//
// 🩸 打洞失败时这决定给用户哪句话：
//
//	一个公网地址都没有 ⇒ 对面根本没探到自己的公网地址（网络挡住了 UDP）
//	  ⇒ 再试一百次也没用，该让他换网络 ✗
//	有公网地址但仍然失败 ⇒ 是"这一对网络"打不通，值得重试 ✓
func HasPublic(cands []string) bool {
	for _, c := range cands {
		host, _, err := net.SplitHostPort(c)
		if err != nil {
			host = c
		}
		if IsPublicIP(host) {
			return true
		}
	}
	return false
}
