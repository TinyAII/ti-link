// 握手识别的测试。
//
// 🩸 这个文件里最要紧的**不是**"能不能认出游戏"，而是"**会不会认错**" ——
// 认错了就会去拦一个正常玩家（拦对了没奖励，拦错了很烦）✗
// 所以有一半用例是"这些都得认不出来" ✓
package game

import (
	"bytes"
	"testing"
)

// 一个真·我的世界 Java 版握手包（1.20.4，协议版本 765）。
//
//	15         包长 21
//	00         包 ID 0（Handshake）
//	FD 05      协议版本 765（VarInt）
//	0E         地址长度 14
//	6D…6D      "mc.example.com"
//	63 DD      端口 25565
//	02         下一状态 2（login）
func mcHandshake() []byte {
	return []byte{
		0x15, 0x00, 0xFD, 0x05, 0x0E,
		'm', 'c', '.', 'e', 'x', 'a', 'm', 'p', 'l', 'e', '.', 'c', 'o', 'm',
		0x63, 0xDD, 0x02,
	}
}

// 泰拉瑞亚的首包：长度 + 类型 + "Terraria"+版本（明文，最好认）
func terrariaHello() []byte {
	return append([]byte{0x0A, 0x00, 0x01}, []byte("Terraria279\x00\x00\x00")...)
}

func TestSniffMinecraft(t *testing.T) {
	if got := Sniff(mcHandshake()); got != "我的世界" {
		t.Fatalf("该认出我的世界，实得 %q", got)
	}
	// 只 peek 到一半（地址没读全）也该认得出 —— 前面几条已经对上了 ✓
	if got := Sniff(mcHandshake()[:12]); got != "我的世界" {
		t.Fatalf("半截握手也该认得出，实得 %q", got)
	}
}

func TestSniffTerraria(t *testing.T) {
	if got := Sniff(terrariaHello()); got != "泰拉瑞亚" {
		t.Fatalf("该认出泰拉瑞亚，实得 %q", got)
	}
}

func TestSniffStardew(t *testing.T) {
	b := append([]byte{0x10, 0x00, 0x00, 0x00}, []byte("StardewValley-1.6.8")...)
	b = append(b, bytes.Repeat([]byte{0}, 8)...)
	if got := Sniff(b); got != "星露谷物语" {
		t.Fatalf("该认出星露谷物语，实得 %q", got)
	}
}

// ⭐ 认不出来的一律返回空串（⇒ 调用方放行）✓
func TestSniffUnknownReturnsEmpty(t *testing.T) {
	cases := map[string][]byte{
		"空":       nil,
		"太短":      {0x15, 0x00},
		"HTTP 请求": []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"),
		"全是零":     bytes.Repeat([]byte{0}, 64),
		"随机二进制":   {0x9f, 0x11, 0x77, 0x02, 0xff, 0x00, 0x13, 0x40, 0x9a, 0x21, 0x03, 0x88},
	}
	for name, b := range cases {
		if got := Sniff(b); got != "" {
			t.Errorf("%s：该认不出来（⇒ 放行），却认成了 %q ✗", name, got)
		}
	}
}

// ⭐⭐ 最要紧的一条：**看着像 MC 但不满足全部四条判据的，不许认成 MC**。
//
// 反证方法：把 sniffMinecraft 里的 ② 或 ③ 那条判断删掉 ⇒ 立刻红 ✓
func TestSniffMinecraftRejectsLookalikes(t *testing.T) {
	good := mcHandshake()

	// ② 包 ID 不是 0（不是 Handshake）⇒ 不认
	notHandshake := append([]byte(nil), good...)
	notHandshake[1] = 0x07
	if got := Sniff(notHandshake); got != "" {
		t.Errorf("包 ID 不是 0 也认成了 %q ✗", got)
	}

	// ③ 协议版本明显不合理（0 / 巨大）⇒ 不认
	for _, ver := range []byte{0x00, 0x7f} {
		bad := append([]byte(nil), good...)
		bad[2] = ver // 协议版本那一字节
		if got := Sniff(bad); got != "" {
			t.Errorf("协议版本字节 0x%02x 也认成了 %q ✗", ver, got)
		}
	}

	// ① 包长离谱（比如 255 —— 真实握手包没这么大）⇒ 不认
	bad := append([]byte(nil), good...)
	bad[0] = 0xFF
	if got := Sniff(bad); got != "" {
		t.Errorf("包长离谱也认成了 %q ✗", got)
	}

	// ④ 地址里有控制字符（真实握手是域名/IP，不会有 \n 之类）⇒ 不认
	bad2 := append([]byte(nil), good...)
	bad2[5] = 0x0A
	if got := Sniff(bad2); got != "" {
		t.Errorf("地址里有控制字符也认成了 %q ✗", got)
	}
}

// ⭐ 泰拉瑞亚的 "Terraria" 必须出现在**前面**才算 ——
// 一个恰好 5 秒后才提到 Terraria 的协议不该被当成它 ✓
func TestSniffTerrariaOnlyNearStart(t *testing.T) {
	b := append(bytes.Repeat([]byte{0x41}, 60), []byte("Terraria")...)
	if got := Sniff(b); got != "" {
		t.Errorf("Terraria 出现在第 60 字节之后也认成了 %q ✗（只看开头）", got)
	}
}
