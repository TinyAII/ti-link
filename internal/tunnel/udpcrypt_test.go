package tunnel

import (
	"bytes"
	"testing"
)

func mustSealer(t *testing.T, key []byte) *udpSealer {
	t.Helper()
	s, err := newUDPSealer(key)
	if err != nil {
		t.Fatalf("建 sealer 失败: %v", err)
	}
	return s
}

func TestUDPSealRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef") // 16 字节，和 keyex 协商出来的一样 ✓
	a, b := mustSealer(t, key), mustSealer(t, key)

	for _, msg := range []string{"hello", "", "一个中文包", string(bytes.Repeat([]byte("x"), 1000))} {
		pkt := a.Seal([]byte(msg))
		plain, ok := b.Open(pkt)
		if !ok {
			t.Fatalf("解不开: %q", msg)
		}
		if string(plain) != msg {
			t.Errorf("来回一趟变了: %q → %q", msg, plain)
		}
	}
}

// 密文里不该出现明文 —— 加密没生效的话这里会红 ✓
func TestUDPSealActuallyEncrypts(t *testing.T) {
	s := mustSealer(t, []byte("0123456789abcdef"))
	pkt := s.Seal([]byte("SECRETSECRET"))
	if bytes.Contains(pkt, []byte("SECRET")) {
		t.Error("密文里能看到明文 —— 等于没加密")
	}
}

// 改一个字节就必须解不开（GCM 的完整性校验）——
// 这条要是过了，说明完整性没生效，那就等于没加密 ✓
func TestUDPSealRejectsTampered(t *testing.T) {
	key := []byte("0123456789abcdef")
	a, b := mustSealer(t, key), mustSealer(t, key)

	pkt := a.Seal([]byte("重要数据"))

	// 逐字节翻一遍，每一个位置都该被拒
	for i := 0; i < len(pkt); i++ {
		bad := append([]byte(nil), pkt...)
		bad[i] ^= 0x01
		if _, ok := b.Open(bad); ok {
			t.Fatalf("第 %d 个字节改了还能解开 —— 完整性没生效", i)
		}
	}

	// 顺手确认原包还是好的（上面那些坏包没污染状态 ✓）
	if _, ok := b.Open(pkt); !ok {
		t.Error("原包该还能解开")
	}
}

// 重放同一个包必须被拒
func TestUDPSealRejectsReplay(t *testing.T) {
	key := []byte("0123456789abcdef")
	a, b := mustSealer(t, key), mustSealer(t, key)

	pkt := a.Seal([]byte("x"))
	if _, ok := b.Open(pkt); !ok {
		t.Fatal("第一次该收下")
	}
	if _, ok := b.Open(pkt); ok {
		t.Error("同一个包再发一次该被拒（重放）")
	}
}

// UDP 会乱序 —— 迟到的包只要是新的，就该收 ✓
func TestUDPSealAcceptsOutOfOrder(t *testing.T) {
	key := []byte("0123456789abcdef")
	a, b := mustSealer(t, key), mustSealer(t, key)

	p1 := a.Seal([]byte("1"))
	p2 := a.Seal([]byte("2"))
	p3 := a.Seal([]byte("3"))

	// 按 3 → 1 → 2 的顺序到
	for _, tc := range []struct {
		pkt  []byte
		want string
	}{
		{p3, "3"}, {p1, "1"}, {p2, "2"},
	} {
		plain, ok := b.Open(tc.pkt)
		if !ok {
			t.Fatalf("乱序的包 %s 该收下", tc.want)
		}
		if string(plain) != tc.want {
			t.Errorf("内容不对: %q", plain)
		}
	}
}

// 太旧的包（掉出窗口）该被拒 —— 不然重放空间就无限大了
func TestUDPSealRejectsTooOld(t *testing.T) {
	key := []byte("0123456789abcdef")
	a, b := mustSealer(t, key), mustSealer(t, key)

	old := a.Seal([]byte("old"))
	// 再发够多，把它挤出窗口
	for i := 0; i < udpReplayWindow+10; i++ {
		a.Seal([]byte("filler"))
	}

	if _, ok := b.Open(old); !ok {
		t.Fatal("第一个包该还是新的")
	}
	// 现在窗口已经很靠前了 ⇒ 再拿一个更旧的来试
	stale := mustSealer(t, key).Seal([]byte("stale")) // 另一个发送方的序号从 0 开始
	if _, ok := b.Open(stale); ok {
		t.Error("序号比窗口还旧的包该被拒")
	}
}

// 两边 key 不一样 ⇒ 必须解不开（不然协商就是白做的）
func TestUDPSealWrongKeyFails(t *testing.T) {
	a := mustSealer(t, []byte("0123456789abcdef"))
	b := mustSealer(t, []byte("fedcba9876543210"))

	if _, ok := b.Open(a.Seal([]byte("x"))); ok {
		t.Error("key 不一样还能解开 —— 加密形同虚设")
	}
}

// 太短 / 太垃圾的包不能把程序搞崩 —— 对面可能是别人扫端口的 ✓
func TestUDPSealGarbageIsSafe(t *testing.T) {
	s := mustSealer(t, []byte("0123456789abcdef"))

	for _, junk := range [][]byte{
		nil, {}, {1}, bytes.Repeat([]byte{0xFF}, 7),
		bytes.Repeat([]byte{0xFF}, 100),
	} {
		if _, ok := s.Open(junk); ok {
			t.Errorf("垃圾包 %v 居然被当成有效包", junk)
		}
	}
}
