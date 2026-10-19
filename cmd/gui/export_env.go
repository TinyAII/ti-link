// 导出日志里的「环境」和「自检」两段。
//
// 为什么要有（2026-10-18）：
//
//	光看连接日志，有一半问题是**看不出来**的 ——
//	"对面什么系统"、"他网卡上到底有没有 IPv6"、"是不是开着加速器"
//	全都不在日志里，只能靠来回问。今晚就为这个耗了半小时 ✗
//	⇒ 导出时自动把环境和四项自检结论带上，用户点一次就把该问的全带走 ✓
//
// 📌 自检要连 STUN，最坏几秒 —— 所以四个检测**并行**跑，别串着等。
package main

import (
	"net"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/windows/registry"

	"p2plink/internal/netutil"
)

// writeSectionText 往纯文本导出里写一段。
func writeSectionText(b *strings.Builder, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	b.WriteString(title + "：\n")
	for _, l := range lines {
		b.WriteString("  " + l + "\n")
	}
}

// writeSectionMD 往 markdown 导出里写一段。
func writeSectionMD(b *strings.Builder, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	b.WriteString("## " + title + "\n\n")
	for _, l := range lines {
		b.WriteString("- " + l + "\n")
	}
	b.WriteString("\n")
}

// envLines 收集运行环境。都是"看一眼就知道对不对"的信息，不含隐私。
func envLines() []string {
	var out []string

	// 系统版本：注册表里最准（GOOS 只知道"windows"）
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		name, _, _ := k.GetStringValue("ProductName")
		disp, _, _ := k.GetStringValue("DisplayVersion")
		build, _, _ := k.GetStringValue("CurrentBuild")
		k.Close()
		// 🩸 Windows 11 在注册表里**仍然写着 "Windows 10"**（微软的历史遗留）。
		//    照抄会让人以为"这系统很老，难怪连不上" ✗
		//    判据：Build ≥ 22000 就是 11 ✓
		if n, err := strconv.Atoi(build); err == nil && n >= 22000 {
			name = strings.Replace(name, "Windows 10", "Windows 11", 1)
		}
		ver := strings.TrimSpace(strings.Join([]string{name, disp}, " "))
		if build != "" {
			ver += "（Build " + build + "）"
		}
		if strings.TrimSpace(ver) != "" {
			out = append(out, "系统："+ver)
		}
	}

	// IPv6 开没开：只看协议有没有启用（有链路本地地址就说明开了）
	if hasLinkLocalV6() {
		out = append(out, "IPv6 协议：已启用")
	} else {
		out = append(out, "IPv6 协议：**已关闭**（网卡属性里没勾）")
	}

	out = append(out, "网卡："+adapterSummary())
	out = append(out, "数据目录："+configDir())
	// 软件版本和会话信息在表头已经有了，这里不重复 ✓
	return out
}

// adapterSummary 概括网卡情况：几张在线、几张有 v4/v6。
// 📌 只报数量，**不列具体名字和 IP** —— 那些对排查没用，还容易带出隐私 ✓
func adapterSummary() string {
	total, withV4, withV6 := 0, 0, 0
	ifaces, err := net.Interfaces()
	if err != nil {
		return "读不到网卡列表"
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}
		total++
		hasV4, hasV6 := false, false
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ipnet.IP.To4() != nil {
				hasV4 = true
			} else if netutil.IsGlobalV6(ipnet.IP) {
				hasV6 = true
			}
		}
		if hasV4 {
			withV4++
		}
		if hasV6 {
			withV6++
		}
	}
	if total == 0 {
		return "没找到可用网卡"
	}
	s := strconv.Itoa(total) + " 张在线"
	if withV4 > 0 {
		s += "，有 IPv4 的 " + strconv.Itoa(withV4) + " 张"
	}
	if withV6 > 0 {
		s += "，有 IPv6 的 " + strconv.Itoa(withV6) + " 张"
	} else {
		s += "，没有任何公网 IPv6"
	}
	return s
}

// diagSnapshot 把四项自检跑一遍，返回几行结论。
//
// 🩸 并行跑：串起来最坏要十几秒，用户点「导出」会以为卡死 ✗
// 📌 每项都只取一句 headline —— 详细原因在日志里，导出里要的是"一眼看结论" ✓
func (b *backend) diagSnapshot() []string {
	lines := make([]string, 4)
	var wg sync.WaitGroup
	wg.Add(4)

	go func() {
		defer wg.Done()
		r := b.NatCheck()
		if s, ok := r["headline"].(string); ok {
			lines[0] = "网络类型：" + s
		}
	}()
	go func() {
		defer wg.Done()
		r := b.IPv6Check()
		if s, ok := r["headline"].(string); ok {
			lines[1] = "IPv6：" + s
		}
	}()
	go func() {
		defer wg.Done()
		r := b.ProxyCheck()
		if s, ok := r["headline"].(string); ok {
			lines[2] = "代理/VPN：" + s
		}
	}()
	go func() {
		defer wg.Done()
		r := b.ServerCheck()
		if s, ok := r["headline"].(string); ok {
			lines[3] = "穿透服务器：" + s
		}
	}()

	wg.Wait()

	out := make([]string, 0, 4)
	for _, l := range lines {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
