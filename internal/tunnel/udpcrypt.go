package tunnel

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"sync"
)

// 给「裸 UDP」那一路做**逐包** AEAD。
//
// 📌 为什么不复用 KCP 那层 AES：KCP 的加密是**流式**的（跨包有状态、按顺序）——
// 而 UDP 每个包都是独立的，丢了、乱了、重复了都不能互相影响 ✗
// 所以这一路得自己加密，而且必须是"一个包自己就能解开"的那种 ✓
//
// 📌 算法用 **AES-128-GCM**（标准库，不加新依赖）：
//   - key 正好 16 字节，跟 internal/keyex 协商出来的一致 ✓
//   - GCM 自带完整性校验（改一个字节就解不开）✓
//
// 包格式：
//
//	[8 字节大端计数器][密文…][16 字节 tag]
//
// 计数器**单调递增** ⇒ nonce 不会重复（这是 AEAD 的硬要求）
// 顺带还能防重放 ✓
//
// ⚠️ 本文件只是能力，还没接进任何流程（见 docs/UDP支持计划.md 阶段 1）✓

// 重放窗口宽度。UDP 会乱序 ⇒ **不能要求严格递增** ✗
// 用一个 64 位的滑动窗口：比窗口还旧的、或者已经收过的，一律拒 ✓
// （跟 DTLS 的思路一样）
const udpReplayWindow = 64

type udpSealer struct {
	aead cipher.AEAD

	mu      sync.Mutex
	sendSeq uint64 // 下一个要用的发送序号

	// 收侧的重放窗口
	recvInit bool
	recvMax  uint64 // 收到过的最大序号
	recvWin  uint64 // 位图：recvMax 往前 64 个包，哪些收过了（bit i 对应 recvMax-i）
}

// newUDPSealer 用协商出来的 key 建一个逐包加密器。key 必须 16 字节 ✓
func newUDPSealer(key []byte) (*udpSealer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &udpSealer{aead: aead}, nil
}

// nonce 由计数器拼出来：前 4 字节留零，后 8 字节放序号 ⇒ 正好 12 字节 ✓
func (s *udpSealer) nonce(dst []byte, seq uint64) []byte {
	dst = append(dst[:0], 0, 0, 0, 0)
	return binary.BigEndian.AppendUint64(dst, seq)
}

// Seal 加密一个包：序号 + 密文 + tag。
// 返回的是可以直接丢给 WriteUDP 的完整字节 ✓
func (s *udpSealer) Seal(plain []byte) []byte {
	s.mu.Lock()
	seq := s.sendSeq
	s.sendSeq++
	s.mu.Unlock()

	out := make([]byte, 8, 8+len(plain)+s.aead.Overhead())
	binary.BigEndian.PutUint64(out, seq)

	var n [12]byte
	return s.aead.Seal(out, s.nonce(n[:0], seq), plain, nil)
}

// Open 解开一个包。
//
// 返回 false 表示"这个包不能用"（坏的 / 重放的 / 太旧的）——
// 调用方**直接丢掉就行**，别当成错误上报：
// UDP 上丢包是常态，为了一个坏包去打断整个转发循环才是真的错 ✗
func (s *udpSealer) Open(pkt []byte) ([]byte, bool) {
	if len(pkt) < 8+s.aead.Overhead() {
		return nil, false
	}
	seq := binary.BigEndian.Uint64(pkt[:8])

	// 🩸 **先解密，解密成功了才记账** —— 这个顺序不能反。
	//
	// 反过来写（先 accept 再解密）会有一个真问题：别人伪造一个包、
	// 只要把计数器改一下，就能**先把那个序号占掉** ⇒
	// 真正的那条包到达时反而被当"重放"拒掉 ✗
	// （2026-10-15 一开始就是这么写的，被 TestUDPSealRejectsTampered 抓到了）
	var n [12]byte
	plain, err := s.aead.Open(nil, s.nonce(n[:0], seq), pkt[8:], nil)
	if err != nil {
		return nil, false // 改过一个字节就会走到这儿 ✓
	}
	// 到这儿的才是真包 ⇒ 现在才动重放窗口 ✓
	if !s.accept(seq) {
		return nil, false
	}
	return plain, true
}

// accept 判断这个序号该不该收 —— 兼顾"乱序要能收"和"重放要拒"。
func (s *udpSealer) accept(seq uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.recvInit {
		s.recvInit, s.recvMax, s.recvWin = true, seq, 1
		return true
	}

	if seq > s.recvMax {
		// 比现在最大的还新 ⇒ 窗口往前挪
		shift := seq - s.recvMax
		if shift >= udpReplayWindow {
			s.recvWin = 0 // 挪过头了，旧的全出窗口
		} else {
			s.recvWin <<= shift
		}
		s.recvWin |= 1
		s.recvMax = seq
		return true
	}

	// 落在窗口内的旧包（乱序到达，正常现象 ✓）
	diff := s.recvMax - seq
	if diff >= udpReplayWindow {
		return false // 太旧了，不要
	}
	bit := uint64(1) << diff
	if s.recvWin&bit != 0 {
		return false // 这个收过了 ⇒ 重放
	}
	s.recvWin |= bit
	return true
}
