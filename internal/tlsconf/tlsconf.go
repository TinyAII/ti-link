// Package tlsconf 造 TLS 配置。
//
// 原来是 `cmd/signal/tls.go` 里的东西 —— 面板也要一模一样的行为（证书续期后按需重读），
// 所以提出来共用一份。**别再复制第二份**：这种"看着简单"的代码一旦分叉，
// 出问题的形式是"一个服务续期后还能用、另一个半年后突然连不上"，极难联想到原因 ✗
package tlsconf

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"os"
	"sync"
	"time"
)

type certReloader struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	modTime time.Time
	size    int64
}

// load 读一次证书。调用方要么持有 mu，要么是启动时的单线程路径。
func (r *certReloader) load() error {
	fi, err := os.Stat(r.certFile)
	if err != nil {
		return fmt.Errorf("读证书失败: %w", err)
	}
	pair, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("加载证书对失败: %w", err)
	}
	r.cert = &pair
	r.modTime = fi.ModTime()
	r.size = fi.Size()
	return nil
}

// get 满足 tls.Config.GetCertificate。
func (r *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	fi, err := os.Stat(r.certFile)
	switch {
	case err != nil:
		// 文件暂时读不到（比如续期中途）：用手里这份先顶着，别把连接全掐了
		if r.cert != nil {
			return r.cert, nil
		}
		return nil, err
	case r.cert == nil || !fi.ModTime().Equal(r.modTime) || fi.Size() != r.size:
		if err := r.load(); err != nil {
			if r.cert != nil {
				return r.cert, nil // 新证书有问题就用旧的，总比没有强
			}
			return nil, err
		}
	}
	return r.cert, nil
}

// New 造一份 TLS 配置。cert/key 任一为空都返回 nil（= 走明文）。
//
// 📌 为什么是"每次握手按需重读"：certbot 续期是**把磁盘上的文件换掉**，
// 而进程里握着的是启动那一刻读进来的证书。不重读的话，续期之后新证书要等到
// 下次重启才生效 —— 而下一次重启可能是半年后，那时候老证书早就过期了，
// 客户端会直接连不上（而且报的是"证书过期"，很难联想到是这个原因）。
// 所以用 GetCertificate：每次握手前看一眼文件有没有变，变了就重读。
// 带上 mtime 缓存，正常情况下就是一次 stat，开销可以忽略。
func New(certFile, keyFile string) (*tls.Config, error) {
	if certFile == "" || keyFile == "" {
		return nil, nil
	}
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if err := r.load(); err != nil {
		return nil, err // 启动时就读不出来，直接失败 —— 别带着"没证书"跑起来
	}
	return &tls.Config{
		GetCertificate: r.get,
		MinVersion:     tls.VersionTLS12,
	}, nil
}

// NewDev 造一份**临时自签**证书，只给本地看界面用。
//
// 🩸 为什么本地非得上 HTTPS：后台的会话 cookie 是 `__Host-` 前缀，
// 而浏览器强制要求这种 cookie 必须 Secure ⇒ 明文 HTTP 下它压根不会被存，
// 表现是"登录点了没反应"，很容易误判成代码坏了 ✗
//
// ⚠️ 这个证书**只认 localhost / 127.0.0.1**，而且没进任何信任链 ——
// 浏览器会报警告，那是正常的、也是应该的 ✓
// 部署的时候**绝不用它**（部署走 certbot 签的真证书）。
func NewDev() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "localhost (本地测试)"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(30 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}, nil
}
