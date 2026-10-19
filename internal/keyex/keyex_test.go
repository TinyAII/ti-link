// 密钥协商的测试。
//
// 这块错一点，表现是"打洞成功但握手失败"—— 很难查，所以宁可在这儿钉死。
package keyex

import (
	"bytes"
	"testing"
)

// 两端各自算出同一把密钥 —— 这是整件事的地基。
func TestSharedKeyMatches(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	b, err := New()
	if err != nil {
		t.Fatal(err)
	}

	ka, err := a.Shared(b.Public())
	if err != nil {
		t.Fatalf("A 算密钥失败: %v", err)
	}
	kb, err := b.Shared(a.Public())
	if err != nil {
		t.Fatalf("B 算密钥失败: %v", err)
	}
	if !bytes.Equal(ka, kb) {
		t.Fatal("两端算出的密钥不一致 —— 隧道永远握不上手")
	}
	if len(ka) != 16 {
		t.Fatalf("密钥该是 16 字节（AES-128），实得 %d", len(ka))
	}
	if !ValidPub(a.Public()) || len(a.Public()) != 32 {
		t.Fatalf("公钥该是 32 字节，实得 %d", len(a.Public()))
	}
}

// 一对一次性密钥，跟不同的人协商出来的密钥必须不同。
// （要是相同，说明根本没把对方公钥用进去 —— 那就是假协商。）
func TestSharedKeyDiffersPerPeer(t *testing.T) {
	me, _ := New()
	p1, _ := New()
	p2, _ := New()

	k1, err := me.Shared(p1.Public())
	if err != nil {
		t.Fatal(err)
	}
	k2, err := me.Shared(p2.Public())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(k1, k2) {
		t.Fatal("跟两个人协商出同一把密钥 —— 对方公钥没参与运算")
	}
}

// 每次生成都不一样（一次性密钥的基本要求）。
func TestNewKeypairIsFresh(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		k, err := New()
		if err != nil {
			t.Fatal(err)
		}
		s := string(k.Public())
		if seen[s] {
			t.Fatal("公钥重复了 —— 随机源有问题")
		}
		seen[s] = true
	}
}

// 对方给垃圾公钥要能挡住，而不是算出个乱七八糟的密钥接着用。
func TestSharedRejectsBadPub(t *testing.T) {
	k, _ := New()
	bad := [][]byte{
		nil,
		{},
		{1, 2, 3},                   // 太短
		bytes.Repeat([]byte{7}, 33), // 太长
		make([]byte, 32),            // 全零（低阶点，X25519 会拒）
	}
	for i, b := range bad {
		if _, err := k.Shared(b); err == nil {
			t.Errorf("第 %d 个坏公钥该被拒，却算出了密钥", i)
		}
	}
}
