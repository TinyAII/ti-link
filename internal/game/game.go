// Package game —— 我们支持的游戏清单，以及"这个端口上是不是某款游戏"的判断。
//
// 🔑 为什么放在 Go 里，而不是只写在前端 JS 里：
// **命令行版也要用同一份判断** ✓
// 前端那份只负责界面显示，真判断在这儿 ✓
//
// 🩸 两份数据必须一致 —— 分叉的表现是"GUI 认得出、命令行认不出" ✗
// 所以有断言盯着它们（见 tmp/check_game.py）✓
package game

import (
	"strings"

	"p2plink/internal/netutil"
)

// Game 是一款我们支持的游戏。
type Game struct {
	Name string

	// ProcMatch 是**进程名关键词**（任意一个命中即算）✓
	//
	// 🩸 为什么是关键词不是完整进程名：这名字会变 ——
	// 游戏更新可能改名、Steam/GOG 版写法可能不同、**玩家自己也能改 exe 文件名** ✓
	// 归一化之后做"包含"匹配，所以写 `stardew` 就能对上
	// "Stardew Valley.exe" / "StardewValley.exe" 等一串写法 ✓
	//
	// ⚠️ **留空 = 我们不知道它叫什么** ⇒ 相关判断一律**放行** ✓
	// 不能因为"我们没资料"就拦人 —— 那是我们的问题，不是用户的 ✗
	ProcMatch []string

	// DualPort = 这款游戏**可能占两个端口**（主 + 一个附加的）。
	//
	// 🩸 目前只有《饥荒》：主世界 UDP 10999 + 洞穴 UDP 10998。
	//    📌 它是**白名单**：服务端只让这些游戏报 `GamePort2`，
	//    别的游戏报了直接拒 —— 不给"任意游戏都能多开一路"的口子 ✓
	DualPort bool
}

// IsDualPort 判断某个游戏名是不是"允许占两个端口"的那一类。
//
// 📌 名字来自客户端上报的 `Msg.Game`，**客户端能撒谎** —— 但它们只能骗到
//
//	自己机器上的第二个端口（服务器只做这一道校验，不转发端口的用途）✓
//
// 📌 名字对不上（老客户端 / 乱填）⇒ 返回 false ⇒ 服务端拒掉第二路 ✓
func IsDualPort(name string) bool {
	for _, g := range All {
		if g.Name == name {
			return g.DualPort
		}
	}
	return false
}

// All 是支持的完整清单。
//
// 📌 加游戏时**前后端都要改**（Go 这份 + 前端 GAMES 那份），
// 有断言盯着两者一致 ✓
var All = []Game{
	// ⚠️ java / javaw 是**很宽泛**的关键词 —— 任何 java 程序都叫这个 ✗
	// ⇒ 挡得住"填 8080 的随便一个程序"，但**挡不住"另一个 java 程序"** ✗
	// 要更严得去读命令行参数（看是不是 -jar server.jar），复杂一档，先不做 ✓
	// （客户端是 javaw，服务端是 java —— 两个都写上 ✓）
	{Name: "我的世界", ProcMatch: []string{"java", "javaw"}},

	{Name: "泰拉瑞亚", ProcMatch: []string{"terraria"}},

	{Name: "星露谷物语", ProcMatch: []string{"stardew"}},

	// 生存战争：完全没资料，不知道它的进程名叫什么 ✓
	// ⇒ 故意留空 ⇒ 相关判断放行 ✓（见上面 ProcMatch 的说明）
	{Name: "生存战争"},

	// 饥荒联机版：Steam 版进程是 dontstarve_steam.exe，WeGame/rail 版是 dontstarve_rail.exe
	// ⇒ 只写 `dontstarve` 这个短关键词，两种都命中 ✓
	{Name: "饥荒联机版", ProcMatch: []string{"dontstarve"}, DualPort: true},

	// 幻兽帕鲁：进程是 Palworld-Win64-Shipping.exe ⇒ 短关键词 `palworld` 就中 ✓
	// 📌 **单端口**（UDP 8211），故意不给 DualPort ✓
	{Name: "幻兽帕鲁", ProcMatch: []string{"palworld"}},

	// 铁锈战争：单端口 5123，**TCP 默认 + UDP 可选**（同端口，不是饥荒那种两个端口）
	// ⇒ 故意**不给** DualPort ✓（它要的是"同端口两种协议"，那由 proto:'both' 搞定）
	{Name: "铁锈战争", ProcMatch: []string{"rusted"}},

	// 方舟：生存进化 —— 🩸 **真的要两路**（第二个用 DualPort 的游戏）
	//   UDP 7777 = 游戏主端口（必需）
	//   UDP 7778 = 7777+1，Raw UDP Socket（建议一起打通）
	//   UDP 27015 = Steam 查询（IP 直连不需要）⇒ 不做 ✗
	//   TCP 27020 = RCON 管理 ⇒ **绝对不放行** ✗（我们不碰）
	// 进程是 ShooterGame.exe ⇒ 关键词 `shootergame`（比 `ark` 准，`ark` 太宽泛）
	{Name: "方舟：生存进化", ProcMatch: []string{"shootergame"}, DualPort: true},

	// 方舟：生存飞升（ASA）—— 前作的重制版，机制变了但端口差不多
	//   进程是 **ArkAscended.exe** ⇒ `arkascended`
	//   🩸 **绝不能跟进化混**：进化是 ShooterGame.exe，两个关键词互不包含 ✓
	//   🩸 发现机制从 Steam 换成 **EOS** ⇒ 服务器列表更难搜到 ⇒ **IP 直连更重要** ✓
	//   27015（Steam 查询）在飞升里可能已弃用；7778 资料里说"可能并非必需"
	//   ⇒ 我们**保守起见仍然打 7778**（成本为零，宁可多开也不漏）✓
	{Name: "方舟：生存飞升", ProcMatch: []string{"arkascended"}, DualPort: true},

	// 像素工厂（Mindustry）：**一个端口 6567，但 TCP 和 UDP 都要**
	//   TCP 6567 = 可靠传输（聊天、建筑操作）
	//   UDP 6567 = 实时同步（玩家位置、单位动作）
	//   ⇒ `proto: 'both'` 正好，**不需要 DualPort**（跟铁锈战争一个模式）
	//   ⚠️ 它的局域网发现走**多播**（227.2.4.7:20151）⇒ 隧道转不了 ⇒ 跨网搜不到
	//      ⇒ 只能引导 IP 直连 ✓
	{Name: "像素工厂", ProcMatch: []string{"mindustry"}},

	// 异星工厂（Factorio）：**纯 UDP 单端口 34197**，没有 TCP 那一半
	//   ⇒ `proto: 'udp'`（不是 both —— 它自己在 UDP 上做了可靠层）✓
	//   ⇒ 跟铁锈/像素工厂那种"两种协议"不一样，别照抄 ✗
	//   ⚠️ 它支持「连接至地址」直连 ✓ 大厅发现跨网靠不住 ⇒ 引导直连 ✓
	{Name: "异星工厂", ProcMatch: []string{"factorio"}},

	// 七日杀（7 Days to Die）—— 主端口 26900 是**双协议**（TCP+UDP 同端口）
	//   ⇒ proto:'both' 管主端口
	//   ⇒ 🩸 还有辅助端口 26901-26903（UDP，Steam 通信/服务器可见性）
	//      我们**只能再开一路** ⇒ 给 26901（DualPort）
	//      26902/26903 这版没转 —— 它们不是直连必经之路，缺了不影响玩家连进去 ✓
	//   ⚠️ 8080-8082（Web 面板 / Telnet）**绝不能碰** ✓ 我们不传它们
	//      但 ports.go 里 **8080 是故意放行的**（我的世界 mod 服要用）
	//      ⇒ 不为此改动：七日杀的玩家填的是 26900，不会填 8080 ✓
	{Name: "七日杀", ProcMatch: []string{"7daystodie", "7dtd"}, DualPort: true},

	// 夜族崛起（V Rising）—— 纯 UDP，游戏 + 查询**两个端口**
	//   新版：27015（游戏）/ 27016（查询）← 默认给这个
	//   旧版：9876 / 9877（部分教程还在用）
	//   ⚠️ RCON（25575 新 / 9878 旧）**绝不碰** ✓
	//   ⚠️ 加入时游戏里可能要勾 **LAN Server** —— 帮助文案里写清 ✓
	{Name: "夜族崛起", ProcMatch: []string{"vrising"}, DualPort: true},
}

// PortVerdict 是一次"端口判定"的结果。
type PortVerdict struct {
	// Game 是命中的游戏名。没命中为空串 ✓
	Game string

	// Known 表示**这次能不能给出确定结论** ✓
	//
	// 🩸 这个字段是整件事的关键（老板定的规矩：宁可不给判断，也别给错的）：
	//
	//	Known=false ⇒ **说不准**（端口上有人，但读不出进程名 —— 多半是权限）
	//	              ⇒ 调用方必须**放行** ✓
	//	Known=true  ⇒ 确定：要么命中了某款游戏（Game 非空），
	//	              要么这里**明确不是**任何游戏（Game 为空）⇒ 可以拦 ✓
	Known bool
}

// VerdictFor 判断「这个端口上是不是**指定的那款**游戏」。
//
// 🩸 这是**给状态灯用的**（"游戏开没开"那一格），**不是**给谁做准入的 ✓
//
// 曾经还有个 `Verdict(port)`（只看"是不是我们列表里任一款"，命令行版用的），
// **已经删了** —— 它当初是为了"建房前拦一下"，而那道拦截整个去掉了，理由见
// cmd/gui/backend.go 里 `CreateRoom` 上方那段：
//
//	改个 exe 文件名就能绕过 ⇒ 挡不住有心的、只挡得住不会改名的，
//	却给了一种"有防护"的错觉 ✗
//
// 📌 所以这儿只留下"**看**"的能力，不再有"**拦**"的 ✓ 两件事别混 ✓
func VerdictFor(port int, gameName string) PortVerdict {
	// 🩸 端口本身不合法 ⇒ 这**没法判成"不是游戏"** ✗ ⇒ 说不准 ✓
	// （少了这一句的话，端口 0 会因为"查不到任何进程"而返回"确定不是游戏"，
	//   上层就会拿这个去拦人 —— 理由还是错的 ✓
	//   2026-10-15 补的时候漏了，被 TestVerdictForRejectsOtherProgram 抓到 ✓）
	if port <= 0 || port > 65535 {
		return PortVerdict{}
	}

	want := Find(gameName)
	if want == nil || len(want.ProcMatch) == 0 {
		// 没有这款游戏，或者我们不知道它的进程名 ⇒ 没资格判断 ⇒ 放行 ✓
		return PortVerdict{}
	}

	paths, readNames, err := processPathsOnPort(port)
	if len(paths) == 0 {
		if err != nil && readNames == 0 {
			return PortVerdict{}
		}
		return PortVerdict{Known: true}
	}
	if readNames == 0 {
		return PortVerdict{}
	}

	for _, p := range paths {
		if nameMatchesAny(p, want.ProcMatch) {
			return PortVerdict{Game: want.Name, Known: true}
		}
	}
	return PortVerdict{Known: true}
}

// Find 按名字找一款游戏。找不到返回 nil ✓
func Find(name string) *Game {
	for i := range All {
		if All[i].Name == name {
			return &All[i]
		}
	}
	return nil
}

// AddrPort 从 "127.0.0.1:24642" 里取端口号 ✓（取不到返回 0）
//
// 📌 转给 netutil 去做 —— 全项目只留**一处**实现 ✓
// （自己再写一份的话，两边的边界判断迟早不一致 ✗）
func AddrPort(addr string) int {
	p, err := netutil.PortOf(addr)
	if err != nil {
		return 0
	}
	return p
}

// NameMatchesAny 进程名和关键词列表比对（归一化之后**包含**就算命中 ✓）
//
// 归一化的好处："stardew" 能同时对上 "Stardew Valley.exe"、"StardewValley.exe"、
// "Stardew Valley (modded).exe" ✓
func NameMatchesAny(path string, match []string) bool { return nameMatchesAny(path, match) }

func nameMatchesAny(path string, match []string) bool {
	got := NameKey(path)
	for _, m := range match {
		if want := NameKey(m); want != "" && strings.Contains(got, want) {
			return true
		}
	}
	return false
}

// NameKey 把进程名归一化成一个"关键词"形式，好做宽松比对。
//
//	"Stardew Valley.exe"    → "stardewvalley"
//	"C:\\...\\Terraria.exe" → "terraria"
//	"SomeMod_Loader.exe"    → "somemodloader"
//
// 📌 为什么归一化（去空格 / 连字符 / 下划线、小写、去 .exe）：
// 同一款游戏的文件名在不同版本、不同平台可能写法不一样 ——
// 写死一个拼法迟早对不上 ✗
func NameKey(path string) string {
	base := strings.ToLower(path)
	if i := strings.LastIndexAny(base, `\/`); i >= 0 {
		base = base[i+1:] // 只留文件名（Windows 和 Unix 两种分隔符都认 ✓）
	}
	base = strings.TrimSuffix(base, ".exe")
	return strings.NewReplacer(" ", "", "-", "", "_", "").Replace(base)
}
