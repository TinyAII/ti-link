// netutil 的测试。
//
// 重点是 MaskAddr —— 它守的是**隐私**：日志会被截图、会被导出发给别人，
// 里面不能出现真的公网地址 ✗ 所以这条要按"性质"钉死，不是对着某个字符串比。
package netutil

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestMaskAddrHidesPublicIP(t *testing.T) {
	cases := []struct{ in, mustKeep string }{
		{"203.0.113.45:25565", "25565"}, // 公网
		{"120.228.149.73:4761", "4761"}, // 真实用过的公网地址
		{"192.168.1.5:25565", "25565"},  // 内网
	}
	for _, c := range cases {
		got := MaskAddr(c.in)
		ip := strings.Split(c.in, ":")[0]
		if strings.Contains(got, ip) {
			t.Errorf("MaskAddr(%q) = %q —— **原地址原样露出来了**", c.in, got)
		}
		if !strings.HasSuffix(got, ":"+c.mustKeep) {
			t.Errorf("MaskAddr(%q) = %q —— 端口该保留（排障要看它）", c.in, got)
		}
	}
}

// 回环地址保持原样：它本来就没隐私价值，打码成 127.0.*.* 反而让日志难读。
func TestMaskAddrKeepsLoopback(t *testing.T) {
	if got := MaskAddr("127.0.0.1:25565"); got != "127.0.0.1:25565" {
		t.Errorf("回环该原样留着，实得 %q", got)
	}
}

func TestMaskAddrs(t *testing.T) {
	got := MaskAddrs([]string{"203.0.113.45:1", "127.0.0.1:2"})
	if strings.Contains(got, "203.0.113.45") {
		t.Errorf("整串里不该出现真地址：%q", got)
	}
	if !strings.Contains(got, "127.0.0.1:2") {
		t.Errorf("回环该原样留着：%q", got)
	}
}

func TestSplitList(t *testing.T) {
	got := SplitList(" a , b ,,  c  , ")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("该切出 %d 项，实得 %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 项该是 %q，实得 %q", i, want[i], got[i])
		}
	}
	if n := len(SplitList("   ")); n != 0 {
		t.Errorf("全是空白该切出 0 项，实得 %d", n)
	}
}

// 本机候选地址：末尾必须有一条回环（同机联调要靠它 ✓），
// 而且每条都得带上我们给的端口。
func TestLocalCandidates(t *testing.T) {
	const port = 25565
	list := LocalCandidates(port)
	if len(list) == 0 {
		t.Fatal("一个候选都没给出来")
	}
	for _, c := range list {
		if !strings.HasSuffix(c, ":"+strconv.Itoa(port)) {
			t.Errorf("候选项该带上端口 %d：%q", port, c)
		}
	}
	found := false
	for _, c := range list {
		if c == "127.0.0.1:25565" {
			found = true
		}
	}
	if !found {
		t.Errorf("候选里该有 127.0.0.1:25565（同机联调要用）：%v", list)
	}
}

// IsPublicIP 是"打洞失败时该说哪句话"的判据，判错了会把人引到错误的方向。
//
// 🩸 尤其 100.64/10（运营商大内网）：Go 的 IsPrivate() **不覆盖**这一段，
// 但它的性质和私网一样 —— 根本没有公网地址，别人打不进来 ✗
func TestIsPublicIP(t *testing.T) {
	cases := []struct {
		host string
		want bool
		why  string
	}{
		{"120.227.55.90", true, "普通公网地址"},
		{"8.8.8.8", true, "普通公网地址"},
		{"192.168.1.5", false, "家庭内网"},
		{"10.1.2.3", false, "私网 10/8"},
		{"172.16.0.1", false, "私网 172.16/12 下界"},
		{"172.31.255.255", false, "私网 172.16/12 上界"},
		{"172.32.0.1", true, "172.32 已经出了私网段"},
		{"127.0.0.1", false, "回环"},
		{"169.254.10.20", false, "链路本地（没拿到 DHCP 时会是这个）"},
		{"0.0.0.0", false, "未指定"},
		{"100.64.0.1", false, "运营商大内网下界"},
		{"100.127.255.255", false, "运营商大内网上界"},
		{"100.128.0.1", true, "100.128 已经出了 CGNAT 段"},
		{"not-an-ip", false, "不是地址"},
	}
	for _, c := range cases {
		if got := IsPublicIP(c.host); got != c.want {
			t.Errorf("IsPublicIP(%q) = %v，期望 %v（%s）", c.host, got, c.want, c.why)
		}
	}
}

// HasPublic 决定"给不给用户希望"：一个公网地址都没有 ⇒ 重试没用。
func TestHasPublic(t *testing.T) {
	if HasPublic([]string{"192.168.1.5:5000", "10.0.0.2:5000"}) {
		t.Error("全是内网地址，却判定有公网")
	}
	if !HasPublic([]string{"192.168.1.5:5000", "120.227.55.90:12189"}) {
		t.Error("有公网地址，却判定没有")
	}
	if HasPublic(nil) {
		t.Error("空列表不该判定有公网")
	}
	// 不带端口也要能认（有的地方传的是纯地址）
	if !HasPublic([]string{"120.227.55.90"}) {
		t.Error("纯地址形式没认出来")
	}
}

// IsGlobalV6 决定"这个 v6 地址要不要发给对方当候选"。
//
// 🩸 判错的后果不是"少一条候选"，而是**给对方发了个废地址**：
//
//	link-local（fe80::）出了本链路就没人认，ULA（fc00::/7）跨网不通，
//	白发一通还拖慢打洞 ✗
func TestIsGlobalV6(t *testing.T) {
	cases := []struct {
		addr string
		want bool
		why  string
	}{
		{"2409:8a50:c31:4870:269:2dff:fef6:1b14", true, "中国移动的公网 v6"},
		{"2606:4700:4700::1111", true, "普通公网 v6"},
		{"2001:db8::1", true, "文档示例段，但仍属全局单播"},
		{"fe80::6107:8230:2499:10ef", false, "链路本地：只在本链路有效"},
		{"fc00::1", false, "ULA 下界"},
		{"fd12:3456:789a::1", false, "ULA（fd00::/8 也在 fc00::/7 里）"},
		{"::1", false, "回环"},
		{"ff02::1", false, "组播"},
		{"::", false, "未指定"},
		{"::ffff:192.168.1.1", false, "IPv4 映射地址，不是真 v6"},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.addr)
		if ip == nil {
			t.Fatalf("测试数据本身有问题，解析不出来：%s", c.addr)
		}
		if got := IsGlobalV6(ip); got != c.want {
			t.Errorf("IsGlobalV6(%s) = %v，期望 %v（%s）", c.addr, got, c.want, c.why)
		}
	}
	if IsGlobalV6(nil) {
		t.Error("nil 不该判成全局地址")
	}
}

// 候选列表的形状：每条都能拆出 host:port，条数不超上限，末尾是回环。
//
// 📌 这台机器可能没有 v6，所以这里**不断言一定有 v6 候选** ——
//
//	只保证“有的话格式对、没的话不影响原来的 v4”。v6 的筛选逻辑由上一条测试钉住 ✓
func TestLocalCandidatesShape(t *testing.T) {
	got := LocalCandidates(27154)
	if len(got) == 0 {
		t.Fatal("一条候选都没有")
	}
	if len(got) > maxLocalCands {
		t.Errorf("候选 %d 条，超过上限 %d（服务端 maxCands=10，超了整条会被拒）", len(got), maxLocalCands)
	}
	if last := got[len(got)-1]; last != "127.0.0.1:27154" {
		t.Errorf("最后一条应该是回环，实际是 %s", last)
	}
	for _, c := range got {
		host, port, err := net.SplitHostPort(c)
		if err != nil {
			t.Errorf("拆不开：%q（%v）", c, err)
			continue
		}
		if port != "27154" {
			t.Errorf("端口不对：%q", c)
		}
		if ip := net.ParseIP(host); ip == nil {
			t.Errorf("host 不是合法地址：%q", c)
			continue
		}
		// v6 必须带方括号，否则对端 SplitHostPort 会拆错
		if strings.Contains(host, ":") && !strings.HasPrefix(c, "[") {
			t.Errorf("v6 候选没加方括号：%q", c)
		}
	}
}

// 候选不能重复。
//
// 多网卡机器（VMware / Hyper-V / 双网口，甚至同一 IP 出现在两个接口上）
// 很容易给出重复项 —— 白打几轮探测包，还挤掉候选条数上限（服务端只收 10 条）。
func TestLocalCandidatesNoDuplicate(t *testing.T) {
	got := LocalCandidates(27154)
	seen := make(map[string]bool, len(got))
	for _, c := range got {
		if seen[c] {
			t.Errorf("候选重复了：%q（重复项没有任何好处，只会占名额）", c)
		}
		seen[c] = true
	}
}

// PublicFamilies：按地址族分开统计"有没有公网地址"。
//
// 🩸 判的是**公网**，不是"有没有这个族的地址" —— 链路本地 v6（fe80::）和
// ULA（fd00::）跨不了网，算进来会让 CrossFamily 误判成"我有 v6" ✗
func TestPublicFamilies(t *testing.T) {
	cases := []struct {
		name         string
		cands        []string
		want4, want6 bool
	}{
		{"只有内网 v4", []string{"192.168.1.5:5000"}, false, false},
		{"公网 v4", []string{"120.227.55.90:12189"}, true, false},
		{"公网 v6", []string{"[2409:8a50:c31::1]:5000"}, false, true},
		{"v4 v6 都有", []string{"120.227.55.90:1", "[2409:8a50::1]:2"}, true, true},
		{"链路本地 v6 不算", []string{"[fe80::1]:5000"}, false, false},
		{"ULA v6 不算", []string{"[fd00::1]:5000"}, false, false},
		{"运营商大内网不算", []string{"100.64.1.5:5000"}, false, false},
		{"空列表", nil, false, false},
	}
	for _, c := range cases {
		has4, has6 := PublicFamilies(c.cands)
		if has4 != c.want4 || has6 != c.want6 {
			t.Errorf("%s：得到 (v4=%v v6=%v)，想要 (v4=%v v6=%v)　候选=%v",
				c.name, has4, has6, c.want4, c.want6, c.cands)
		}
	}
}

// CrossFamily：只判"一个人有 IPv6、另一个人没有"这一种死局。
//
// 🩸 反证方法：把 CrossFamily 的返回值改成 `peer6, my6`（去掉取反）
// ⇒ 下面"两边都有 v6"那条立刻红 ✓
func TestCrossFamily(t *testing.T) {
	v4 := []string{"120.227.55.90:1"}
	v6 := []string{"[2409:8a50::1]:2"}
	both := []string{"120.227.55.90:1", "[2409:8a50::1]:2"}

	// 对方有 v6、我没有 ⇒ 他唯一的路我接不住
	if p, m := CrossFamily(v4, v6); !p || m {
		t.Errorf("我没有 v6、对方有：应该 peerHas6Only=true，实际 p=%v m=%v", p, m)
	}
	// 反过来
	if p, m := CrossFamily(v6, v4); p || !m {
		t.Errorf("我有 v6、对方没有：应该 myHas6Only=true，实际 p=%v m=%v", p, m)
	}
	// 两边都有 v6 ⇒ 有共同语言，不是死局
	if p, m := CrossFamily(v6, v6); p || m {
		t.Error("两边都有 v6，不该判成跨族 —— v6 这条路本来是通的")
	}
	// 两边都只有 v4 ⇒ 也是共同语言（v4 是所有人的底线）
	if p, m := CrossFamily(v4, v4); p || m {
		t.Error("两边都只有 v4，不该判成跨族")
	}
	// 我有双栈、对方只有 v4 ⇒ myHas6Only=true。
	//
	// 📌 这**不是**误判：能走到 CrossFamily 就说明 v4 那条**已经打不通了**
	// （它只在打洞失败之后被调用），所以"你有 v6、他没有"确实是他唯一能改的东西 ✓
	// 文案给的是"让他换个能拿到 v6 的网络"，对用户是有用的话 ✗ 不是叫他去走 v4
	if p, m := CrossFamily(both, v4); p || !m {
		t.Errorf("我有双栈、对方只有 v4：应该 myHas6Only=true，实际 p=%v m=%v", p, m)
	}
	// 反过来同理
	if p, m := CrossFamily(v4, both); !p || m {
		t.Errorf("对方双栈、我只有 v4：应该 peerHas6Only=true，实际 p=%v m=%v", p, m)
	}
}
