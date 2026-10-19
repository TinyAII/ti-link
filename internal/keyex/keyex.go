// Package keyex 做临时密钥协商：两端各自生成一对一次性密钥，
// 把**公钥**经由信令服务器转给对方，各自算出同一把会话密钥。
//
// 为什么不用「房间密码当密钥」那套（原来就是这么做的）：
//
//	· 密码是人打的，熵很低；密钥 = sha256(固定前缀 + 密码)，没有 KDF、没有迭代，
//	  抓到一个包就能离线爆破；
//	· 更要命的是**没设密码时它会退回到服务器访问口令** —— 而那个口令硬编码在
//	  客户端里，等于全网公开 ⇒ 那种房间的"加密"其实一点用都没有。
//
// 换成 ECDH 之后：**服务器只见公钥、算不出会话密钥**，房间密码回归它本职
// （只决定"放不放你进来"），密码设得再弱也不影响机密性。
//
// 配合信令通道的 TLS：TLS 挡住路径上的人换公钥，ECDH 挡住服务器自己看内容。
// 两个缺一不可 —— 只有 ECDH 的话，能在信令中间做手脚的人把公钥一换就全解开了。
package keyex

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

// info 是 HKDF 的用途标签。换它等于换一套密钥，将来协议升级时用得上。
const info = "p2plink-tunnel-v2"

// pubLen X25519 公钥固定 32 字节。收到别的不合法长度直接拒。
const pubLen = 32

// Keypair 是一对一次性密钥。
type Keypair struct {
	priv *ecdh.PrivateKey
}

// New 生成一对。每次联机都该重新生成 —— 它是一次性的。
func New() (*Keypair, error) {
	p, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成密钥对失败: %w", err)
	}
	return &Keypair{priv: p}, nil
}

// Public 取公钥（32 字节），发给对方。
func (k *Keypair) Public() []byte { return k.priv.PublicKey().Bytes() }

// Shared 拿对方公钥算出 16 字节会话密钥（给 KCP 的 AES-128 用）。
// 两端算出来必然相同；交换过程只经过公钥，服务器推不出这把密钥。
func (k *Keypair) Shared(peerPub []byte) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(peerPub)
	if err != nil {
		return nil, fmt.Errorf("对方公钥不合法: %w", err)
	}
	secret, err := k.priv.ECDH(pub)
	if err != nil {
		return nil, fmt.Errorf("协商失败: %w", err)
	}
	// 用 HKDF 把共享点压成定长密钥。X25519 的原始输出虽然也能用，
	// 但它不是均匀随机的，过一道 KDF 才是标准做法。
	key, err := hkdf.Key(sha256.New, secret, nil, info, 16)
	if err != nil {
		return nil, fmt.Errorf("派生密钥失败: %w", err)
	}
	return key, nil
}

// ValidPub 检查一个公钥长度对不对（服务端和我们自己都要挡）。
func ValidPub(b []byte) bool { return len(b) == pubLen }
