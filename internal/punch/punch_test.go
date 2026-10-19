// 候选地址白名单的测试。
//
// 候选项是**对方**给的，而我们要往它发包 —— 所以这里照着"对方想害我们"
// 来写用例，而不是照着正常客户端会发什么写。
package punch

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"p2plink/internal/netutil"
)

func TestUsableCand(t *testing.T) {
	ok := []string{
		"1.2.3.4",         // 普通公网地址
		"192.168.1.5",     // 内网（真机联机时常见）
		"10.0.0.7",        // 内网
		"127.0.0.1",       // 回环 —— 本机 e2e 靠它
		"100.64.0.1",      // CGNAT 段，运营商大内网常见
		"223.255.255.254", // 公网边界内

		// 🆕 2026-10-18 起支持 IPv6。
		//    📌 这条原来是"该拒"（"我们只打 udp4"），现在正式翻过来了 ——
		//    不是测试写错，是行为改了，改的时候要说得清 ✓
		"2001:db8::1",                           // 全局单播（文档段）
		"2409:8a50:c31:4870:269:2dff:fef6:1b14", // 真实的中国移动公网 v6
		"::1",                                   // v6 回环 —— 跟 127.0.0.1 一个道理
	}

	// v6 里"看着像地址、其实发出去没用"的，必须拒掉
	v6bad := []struct{ s, why string }{
		{"fe80::1", "链路本地：只在本链路有效，发给对面等于废地址"},
		{"fc00::1", "ULA 下界：内网自留段，跨网不通"},
		{"fd12:3456::1", "ULA：同上"},
		{"ff02::1", "组播"},
		{"::", "未指定地址"},
	}
	for _, s := range ok {
		if !usableCand(s) {
			t.Errorf("%s 该被接受，却被拒了", s)
		}
	}

	bad := []struct{ s, why string }{
		{"example.com", "域名 —— 让我们替对方解析，可以当 DNS 放大器"},
		{"localhost", "域名（而且指向本机）"},
		{"1.2.3.4.nip.io", "域名（伪装成 IP 的也拒）"},
		{"0.0.0.0", "未指定地址"},
		{"255.255.255.255", "广播地址"},
		{"224.0.0.1", "组播"},
		{"239.255.255.250", "组播（SSDP）"},
		{"240.0.0.1", "保留段"},
		{"", "空串"},
		{"   ", "空白"},
		{"1.2.3.4;rm -rf /", "夹带奇怪字符"},
	}
	bad = append(bad, v6bad...)
	for _, c := range bad {
		if usableCand(c.s) {
			t.Errorf("%q 该被拒（%s），却放行了", c.s, c.why)
		}
	}

	// 超长输入也别让它有可乘之机
	if usableCand(strings.Repeat("1", 100000)) {
		t.Error("超长输入该被拒")
	}
}

// 整条候选（带端口）走一遍：域名要在这里就被拦掉，
// 不能等到 ResolveUDPAddr 去替对方做 DNS。
func TestPunchRejectsHostnameCandidates(t *testing.T) {
	cands := []string{
		"example.com:25565",
		"localhost:25565",
		"0.0.0.0:25565",
		"224.0.0.1:25565",
	}
	// 直接查白名单，不起真连接（Punch 要真 UDP 套接字，这里只验过滤规则）
	kept := 0
	for _, c := range cands {
		host, _, err := net.SplitHostPort(c)
		if err != nil || !usableCand(host) {
			continue
		}
		kept++
	}
	if kept != 0 {
		t.Errorf("这几个候选该被全拦，却留下了 %d 个", kept)
	}
}

// Explain 决定用户看到的是哪句话 —— 说错了会把人引到错误的自救动作上。
//
// 🩸 原来弹窗直接甩 `打洞失败: read udp4 0.0.0.0:58782: i/o timeout`：
// 用户看不懂，而且 0.0.0.0 是自己这边的监听地址，容易被误读成"对方是 0.0.0.0" ✗
func TestExplain(t *testing.T) {
	// ① 对方一个公网地址都没有 ⇒ 必须说"换网络"，不能给"再试试"的希望
	only := []string{"192.168.1.5:5000", "10.0.0.7:5000"}
	msg := Explain("对方", only, nil, 25*time.Second).Error()
	if !strings.Contains(msg, "没探到公网地址") {
		t.Errorf("对方只有内网地址时应该点明公网地址缺失，实际：%s", msg)
	}
	// 📌 别死抠某一句：文案会改，但"给出可执行的动作"这个性质不能丢
	if !strings.Contains(msg, "换") || !strings.Contains(msg, "网络") {
		t.Errorf("应该给出可执行的动作（换网络），实际：%s", msg)
	}
	// 不能把原始错误原样带出去
	if strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "udp4") {
		t.Errorf("不该把 Go 的原始网络错误甩给用户：%s", msg)
	}

	// ② 对方有公网地址 ⇒ 是"这一对网络"打不通，可以说"换网络/热点"但别说死
	hasPub := []string{"192.168.1.5:5000", "120.227.55.90:12189"}
	msg2 := Explain("「小明」", hasPub, nil, 25*time.Second).Error()
	if !strings.Contains(msg2, "「小明」") {
		t.Errorf("房主侧应该带上对方的名字：%s", msg2)
	}
	if !strings.Contains(msg2, "25 秒") {
		t.Errorf("应该说清等了多久：%s", msg2)
	}
	if strings.Contains(msg2, "没探到公网地址") {
		t.Errorf("有公网地址却说他没探到，会把人引错方向：%s", msg2)
	}

	// ③ 有公网地址时，要给出**按成功率排的**具体动作，不能只说"检查网络"
	for _, want := range []string{"VPN", "加速器", "热点"} {
		if !strings.Contains(msg2, want) {
			t.Errorf("失败引导里缺了「%s」这一条 —— 用户不知道该怎么办：%s", want, msg2)
		}
	}
	// 保底那条必须说清"两边都连同一个热点"才算真话：
	// 只说"开热点"的话，对方不连上去照样不通 ✗
	if !strings.Contains(msg2, "两边都连") {
		t.Errorf("热点那条要写清两边都连，否则用户会理解成只有一方换网：%s", msg2)
	}

	// ④ 超时时间要按实际值说，不是写死
	if msg3 := Explain("对方", hasPub, nil, 10*time.Second).Error(); !strings.Contains(msg3, "10 秒") {
		t.Errorf("超时秒数没跟着走：%s", msg3)
	}
}

// 「他有 IPv6、我没有」—— 这一档**不能**再给那三条通用建议。
//
// 🩸 2026-10-18 老板朋友的真实情况：他对称型 NAT + 公网 IPv6，老板只有 IPv4。
// 原来这种情况下弹出的建议是「关 VPN / 换成 IPv6 再试 / 开热点」——
// 前两条**全是空话**（缺的是地址，不是开关），只有第三条碰巧对 ✗
//
// 反证方法：把 Explain 里 `if peerHas6Only` 那段删掉 ⇒ 这个测试红 ✓
func TestExplainCrossFamily(t *testing.T) {
	myV4 := []string{"192.168.1.5:5000", "120.227.55.90:12189"}
	peerV6 := []string{"192.168.1.9:5000", "[2409:8a50:c31::1]:5000"}

	msg := Explain("「小李」", peerV6, myV4, 25*time.Second).Error()
	// 必须点明"你有他没有"这件事本身
	if !strings.Contains(msg, "IPv6") {
		t.Errorf("跨族失败必须提到 IPv6：%s", msg)
	}
	if !strings.Contains(msg, "你这边没有") {
		t.Errorf("要说清是**谁**缺 v6，否则用户不知道该动哪台机器：%s", msg)
	}
	// 必须给出**真正有用**的两条：自己换网络拿到 v6 / 让对方换网络
	if !strings.Contains(msg, "热点") {
		t.Errorf("没给「开热点验证 v6」这条 —— 那是当天就能做的：%s", msg)
	}
	// 🩸 最要紧：必须明说「关 VPN 没用」，否则用户会照着习惯去关加速器白折腾
	if !strings.Contains(msg, "没用") {
		t.Errorf("没说清「关 VPN 这类动作没用」，用户会白折腾：%s", msg)
	}
	// 不能还是那套通用三条（"按顺序试"是通用那条的开头）
	if strings.Contains(msg, "按顺序试") {
		t.Errorf("跨族场景不该再甩通用建议：%s", msg)
	}

	// 反过来：我有 v6、他没有 ⇒ 说的是另一套话（让他换网络 / 两边连同一个网络）
	rev := Explain("「小李」", myV4, peerV6, 25*time.Second).Error()
	if !strings.Contains(rev, "只有你能发给他") && !strings.Contains(rev, "他发不回来") {
		t.Errorf("反向跨族的说法不对：%s", rev)
	}

	// 两边都没有 v6 ⇒ 还是通用那套（别误判成跨族）
	same := Explain("「小李」", myV4, myV4, 25*time.Second).Error()
	if strings.Contains(same, "你这边没有") {
		t.Errorf("两边都没有 v6，不该说成跨族：%s", same)
	}
	if !strings.Contains(same, "按顺序试") {
		t.Errorf("两边都没有 v6 时该走通用建议：%s", same)
	}
}

// v6 打洞：在本机回环上真跑一遍。
//
// 📌 这**不是**"真实 v6 环境验证" —— 回环没有 NAT、也不跨网。
//
//	但它能证明：v6 地址能被解析、能通过双栈 socket 发出去、能收回来、
//	能完成"收到对方探测包"那一步 ✓
//	也就是**代码路径是通的**，剩下的只是真实网络里能不能到达。
//
// 🩸 没有这条测试的话，"支持 v6"就只是"候选列表里多了个字符串"，
//
//	实际发包那一步有没有接上完全不知道 ✗
func TestPunchOverIPv6Loopback(t *testing.T) {
	a, err := netutil.ListenDual(0)
	if err != nil {
		t.Fatalf("开双栈 socket 失败: %v", err)
	}
	defer a.Close()
	b, err := netutil.ListenDual(0)
	if err != nil {
		t.Fatalf("开第二个 socket 失败: %v", err)
	}
	defer b.Close()

	pa := a.LocalAddr().(*net.UDPAddr).Port
	pb := b.LocalAddr().(*net.UDPAddr).Port
	noop := func(_, _ string) {}

	type res struct {
		from *net.UDPAddr
		err  error
	}
	ch := make(chan res, 2)

	go func() {
		from, err := Punch(a, []string{net.JoinHostPort("::1", strconv.Itoa(pb))},
			3*time.Second, "v6-loopback", noop)
		ch <- res{from, err}
	}()
	go func() {
		from, err := Punch(b, []string{net.JoinHostPort("::1", strconv.Itoa(pa))},
			3*time.Second, "v6-loopback", noop)
		ch <- res{from, err}
	}()

	for i := 0; i < 2; i++ {
		r := <-ch
		if r.err != nil {
			t.Fatalf("v6 回环打洞失败: %v（说明 v6 地址根本没走通）", r.err)
		}
		if r.from == nil || r.from.IP.To4() != nil {
			t.Errorf("打洞成功但来源不是 v6 地址：%v", r.from)
		}
	}
}

// PunchEx 的两个新能力：报进度、能取消。
//
// 为什么要测：
//
//	· 进度不报 ⇒ 界面一直停在"打洞中…"，用户以为卡死（老板提的）
//	· 不能取消 ⇒ 用户退出房间后还要干等满 25 秒才释放 ✗
func TestPunchExTickAndCancel(t *testing.T) {
	// ① 报进度：把超时压到 700ms，应该至少 tick 一次
	c1, err := netutil.ListenDual(0)
	if err != nil {
		t.Fatalf("建 socket 失败: %v", err)
	}
	defer c1.Close()

	ticks := 0
	// 指向一个**没人听**的端口，保证打不通
	dead := net.JoinHostPort("127.0.0.1", strconv.Itoa(freePort()))
	_, err = PunchEx(c1, []string{dead}, 700*time.Millisecond, "tok", func(_, _ string) {},
		func(_, _ time.Duration) { ticks++ }, nil)
	if !errors.Is(err, errPunchTimeout) {
		t.Errorf("应该因为超时返回，实际：%v", err)
	}
	if ticks == 0 {
		t.Error("一次进度回调都没有 —— 界面上永远停在「打洞中…」，用户会以为卡死")
	}

	// ② 取消：canceled 立刻为真 ⇒ 马上收手，不等满超时
	c2, err := netutil.ListenDual(0)
	if err != nil {
		t.Fatalf("建 socket 失败: %v", err)
	}
	defer c2.Close()

	start := time.Now()
	_, err = PunchEx(c2, []string{dead}, 5*time.Second, "tok", func(_, _ string) {},
		nil, func() bool { return true })
	if !errors.Is(err, ErrCanceled) {
		t.Errorf("应该返回 ErrCanceled，实际：%v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("取消后 %v 才返回 —— 退出房间要等这么久才释放，太久", d)
	}
}

// freePort 找一个当前没人监听的端口（关掉后立刻返回，够测试用）。
func freePort() int {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return 9
	}
	p := c.LocalAddr().(*net.UDPAddr).Port
	c.Close()
	return p
}
