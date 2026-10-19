// 握手识别：看连接**开头那几个字节**，认出"连进来的是哪款游戏"。
//
// 🩸 这跟同一目录下 ProcMatch 那条路（看**进程名**）性质完全不同，别混：
//
//	进程名（ProcMatch）  —— 改个 exe 名字就绕过了；而且玩家用第三方启动器、
//	                        换版本时名字也会变 ⇒ **可能误伤正常人** ✗
//	                        （老板当年就是拿这个理由把"用进程名拦建房"整个删掉的 ✓）
//	握手数据（本文件）    —— 看的是**数据本身**。换启动器、改文件名都不影响它 ✓
//	                        想伪装得同时改客户端 ⇒ 误伤风险低得多 ✓
//
// ⚠️ 但它**仍然不是防线**：改客户端的人可以照着真游戏的握手伪造一份 ✗
//
//	它的定位是「**给出事实依据**」—— 用来提示用户 / 本地拒接错的游戏 ✓
//
// 📌 **认不出来就返回空串 ⇒ 调用方一律放行** ✓
//
//	我们没资料（比如"生存战争"连进程名都不知道）是我们的问题，不是用户的 ✗
package game

import "strings"

// Sniff 看一段连接开头的字节，认出是哪款游戏；认不出来返回空串 ✓
//
// 📌 调用方应该 peek（**不要吃掉这些字节**）—— 它们是游戏自己的握手，
// 读走了游戏就连不上了 ✗（要读多少：64 字节够我们判全部三条特征）
//
// 📌 判据一律**保守**：宁可认不出来（⇒ 放行），也不要认错（⇒ 拦错人）✗
func Sniff(b []byte) string {
	if len(b) == 0 {
		return ""
	}

	// ① 泰拉瑞亚：明文首包里就有 "Terraria"
	//
	// 它的 ConnectRequest 长这样：[int16 长度][1 字节类型][字符串 "Terraria"+版本]
	// ⇒ 出现这串几乎不可能是别的游戏碰巧撞上 ✓ 特征最强，所以排第一个 ✓
	if len(b) >= 16 && containsASCII(b[:min(len(b), 48)], "Terraria") {
		return "泰拉瑞亚"
	}

	// ② 星露谷物语（Lidgren 库）：握手里带库名或游戏名。
	//
	// 🩸 这条**弱** —— 我们只是见过 "Stardew" 字样，不保证每个版本都带 ✗
	// 所以放在后面、只看前 64 字节，而且**认出来就当线索**（不单独拿去拦人）✓
	if len(b) >= 16 && (containsFold(b[:min(len(b), 64)], "stardew") ||
		containsFold(b[:min(len(b), 64)], "lidgren")) {
		return "星露谷物语"
	}

	// ③ 我的世界 Java 版：Handshake 包的结构最好认
	if sniffMinecraft(b) {
		return "我的世界"
	}

	return ""
}

// sniffMinecraft 认 MC 的 Handshake 包：
//
//	VarInt 包长 | VarInt 包 ID(=0x00) | VarInt 协议版本 | String 地址 | uint16 端口 | VarInt 下一状态
//
// 🩸 判据刻意**四条一起要**：只凭"第二个字节是 0"会撞上一大堆二进制协议 ✗
// （我们宁可认不出来 ⇒ 放行，也不能把它认成 MC）
func sniffMinecraft(b []byte) bool {
	if len(b) < 8 {
		return false
	}
	// ① 包长是个"小" VarInt —— 真实握手包 10~40 字节（留宽到 6~120）
	if b[0] < 6 || b[0] > 120 {
		return false
	}
	// ② 包 ID 必须是 0（Handshake）
	if b[1] != 0x00 {
		return false
	}
	// ③ 协议版本 VarInt 落在合理范围（1.7 = 4 … 1.21 = 767，留到 4096）
	ver, n := readVarInt(b[2:])
	if n == 0 || ver < 4 || ver > 4096 {
		return false
	}
	// ④ 跟着是"服务器地址"：长度 VarInt + 可打印字符
	i := 2 + n
	if i >= len(b) {
		return false
	}
	addrLen, m := readVarInt(b[i:])
	if m == 0 || addrLen <= 0 || addrLen > 255 {
		return false
	}
	j := i + m
	if j+int(addrLen) > len(b) {
		// 地址还没 peek 全 —— 前面四条都对得上，这已经够了 ✓
		return true
	}
	for _, c := range b[j : j+int(addrLen)] {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// readVarInt 解 MC 那种 VarInt（每字节低 7 位，最高位是"还有下一字节"）。
// 返回（值, 用掉几个字节）；解不动返回 (0, 0) ✓
//
// 📌 最多 5 字节 —— MC 的 VarInt 就是这个上限，超了说明这不是它 ✗
func readVarInt(b []byte) (int, int) {
	v, shift := 0, 0
	for i := 0; i < len(b) && i < 5; i++ {
		c := b[i]
		v |= int(c&0x7f) << shift
		if c&0x80 == 0 {
			return v, i + 1
		}
		shift += 7
	}
	return 0, 0
}

func containsASCII(b []byte, s string) bool { return strings.Contains(string(b), s) }

// containsFold 大小写不敏感（游戏名在不同版本里大小写会变：Stardew / stardew ✓）
func containsFold(b []byte, s string) bool {
	return strings.Contains(strings.ToLower(string(b)), strings.ToLower(s))
}
