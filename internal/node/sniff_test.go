// sniffGame 的测试：加入方"认一下连进来的是什么游戏"。
//
// 🩸 这个文件里最要紧的一条是「**peek 没把数据吃掉**」——
// 真读走了的话，游戏客户端就再也连不上了，而且症状会非常难查
// （"连不上" 和 "被嗅探过" 之间看不出关系）✗
package node

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// 一段泰拉瑞亚的握手（明文里有 "Terraria"）+ 补到 64 字节
func terrariaPayload() []byte {
	b := append([]byte{0x0A, 0x00, 0x01}, []byte("Terraria279")...)
	return append(b, bytes.Repeat([]byte{0}, 64-len(b))...)
}

func mkRunner(mine string) (*Runner, *[]string) {
	got := &[]string{}
	r := &Runner{cfg: Config{Game: mine}}
	r.OnNotice = func(s string) { *got = append(*got, s) }
	return r, got
}

// pipeWith 造一条"对面写好了数据"的连接 ✓
func pipeWith(t *testing.T, payload []byte) (net.Conn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	go func() {
		_, _ = b.Write(payload)
		// 不关：关掉的话"peek 完整性"那条会读到 EOF，分不清是吃掉了还是对面走了 ✗
	}()
	return a, b
}

// ⭐⭐ 认出来是**别的**游戏 ⇒ 这条连接要被**掐掉**（返回 nil + 真的断开）。
//
// 🩸 这跟服务端那道管的事不一样：服务端看**自报的名字**，这道看**握手数据** ⇒
// 它兜的是"自报蒙对了、实际在开别的游戏"那种 ✓
//
// 反证方法：把 `out.Close(); return nil` 改成 `return out` ⇒ 立刻红 ✓
func TestSniffGameDropsMismatch(t *testing.T) {
	a, peer := pipeWith(t, terrariaPayload())
	r, got := mkRunner("我的世界")

	if c := r.sniffGame(a); c != nil {
		c.Close()
		t.Fatal("认出别的游戏时该返回 nil（这条连接要掐掉），实得非 nil ✗")
	}

	// 得给玩家一句说明，而且说清"连进来的是什么、这个房间开的是什么"
	if len(*got) != 1 {
		t.Fatalf("该给一句说明，实得 %d 句：%v", len(*got), *got)
	}
	for _, want := range []string{"泰拉瑞亚", "我的世界"} {
		if !strings.Contains((*got)[0], want) {
			t.Errorf("说明里没提 %q：%s", want, (*got)[0])
		}
	}

	// ⭐ 而且**连接真的断了**：对面读该拿到 EOF。
	// 📌 只"返回 nil 但没 close"是抓得出来的 —— 那种情况这儿会读超时 ✓
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 8)
	if _, err := peer.Read(buf); err == nil {
		t.Fatal("连接还开着（读到了数据）—— 光返回 nil 不算掐掉 ✗")
	} else if !errors.Is(err, io.EOF) {
		t.Fatalf("对面该读到 EOF，实得 %v —— 超时的话说明连接没断 ✗", err)
	}
}

// ⭐⭐ peek **不许**把数据吃掉：从返回的连接里读，必须拿到**原样**的完整数据。
//
// 反证方法：把 sniffGame 里的 `br.Peek(64)` 换成 `br.Read` ⇒ 立刻红 ✓
func TestSniffGameDoesNotEatData(t *testing.T) {
	payload := terrariaPayload()
	// 📌 用**匹配的**游戏名：这条测的是「peek 不吃数据」，
	// 名字对不上会被掐掉，那就测不到数据完不完整了 ✗（两件事分开测）
	a, _ := pipeWith(t, payload)
	r, _ := mkRunner("泰拉瑞亚")

	c := r.sniffGame(a)
	defer c.Close()

	gotBuf := make([]byte, len(payload))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, gotBuf); err != nil {
		t.Fatalf("读不出完整数据：%v ✗（peek 吃掉了？）", err)
	}
	if !bytes.Equal(gotBuf, payload) {
		t.Fatal("读回来的数据跟写进去的不一样 ✗（peek 把开头吃掉/弄乱了）")
	}
}

// 认出来的就是自己选的那款 ⇒ 不用打扰（正常情况占大多数）✓
func TestSniffGameSilentWhenMatches(t *testing.T) {
	a, _ := pipeWith(t, terrariaPayload())
	r, got := mkRunner("泰拉瑞亚")
	c := r.sniffGame(a)
	defer c.Close()

	if len(*got) != 0 {
		t.Errorf("自己就选的泰拉瑞亚，不该提示：%v", *got)
	}
}

// 认不出（随机数据）⇒ 一句都不说（我们没资料不是用户的错）✓
func TestSniffGameSilentWhenUnknown(t *testing.T) {
	a, _ := pipeWith(t, bytes.Repeat([]byte{0xA5}, 64))
	r, got := mkRunner("我的世界")
	c := r.sniffGame(a)
	defer c.Close()

	if len(*got) != 0 {
		t.Errorf("认不出的时候不该提示：%v", *got)
	}
}

// 自己没选游戏（老客户端）⇒ 不比、也不提示 ✓
func TestSniffGameSilentWhenMineEmpty(t *testing.T) {
	a, _ := pipeWith(t, terrariaPayload())
	r, got := mkRunner("")
	c := r.sniffGame(a)
	defer c.Close()

	if len(*got) != 0 {
		t.Errorf("自己没报游戏的时候不该提示：%v", *got)
	}
}

// ⭐ 对面**一声不吭**（服务端先开口那种协议）⇒ 不许把连接卡住。
//
// 📌 这条会真的等一个 sniffTimeout（300ms）—— 故意的：
// 我们要的就是"等不到就走"，所以得验证它**真的会走** ✓
func TestSniffGameDoesNotHangOnSilentPeer(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	r, got := mkRunner("我的世界")

	start := time.Now()
	c := r.sniffGame(a)
	defer c.Close()
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("对面没发数据时卡了 %v ✗（超时没生效？）", elapsed)
	}
	if len(*got) != 0 {
		t.Errorf("没数据可看时不该提示：%v", *got)
	}
	// 而且超时**要清掉** —— 不然接下来正常转发会被这个 deadline 掐断 ✗
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("清 deadline 失败：%v", err)
	}
}
