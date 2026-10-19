package tlsconf_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"p2plink/internal/tlsconf"
)

// 两个都不给 ⇒ 返回 nil（= 走明文），不是报错。
func TestTLSDisabledWhenEmpty(t *testing.T) {
	cfg, err := tlsconf.New("", "")
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	if cfg != nil {
		t.Fatal("没给证书该返回 nil（走明文）")
	}
	// 只给一半也算没配
	if cfg, _ := tlsconf.New("a.pem", ""); cfg != nil {
		t.Fatal("只给证书不给私钥，也该当成没配")
	}
}

// 启动时读不出来就得**直接失败** —— 不能带着"没证书"跑起来。
func TestTLSFailsLoudOnBadCert(t *testing.T) {
	_, err := tlsconf.New(filepath.Join(t.TempDir(), "nope.pem"), filepath.Join(t.TempDir(), "nope.key"))
	if err == nil {
		t.Fatal("证书不存在该报错 ✗（别带着没证书的状态跑起来）")
	}
}

// ⭐⭐ 这条测的是这个包**存在的理由**：证书文件被换掉之后，
// 下一次握手要用到新的那张。
//
// 背景：certbot 续期就是"把磁盘上的文件换掉"，而进程里握着的是启动那一刻
// 读进来的证书。不重读的话，新证书要等到下次重启才生效 ——
// 而下一次重启可能是半年后，那时候老证书早过期了，客户端直接连不上，
// 报的还是"证书过期"这种完全联想不到原因的错 ✗
//
// 🩸 反证方法：把 certReloader.get 里那段"mtime 变了就重读"的判断删掉，
// 这条立刻红 ✓
func TestCertHotReload(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	certA, keyA := selfSignedPEM(t, "第一张")
	writeFile(t, certPath, certA)
	writeFile(t, keyPath, keyA)

	cfg, err := tlsconf.New(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	got1, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}

	// 换一张新证书（模拟 certbot 续期）
	certB, keyB := selfSignedPEM(t, "第二张")
	writeFile(t, certPath, certB)
	writeFile(t, keyPath, keyB)
	// 🩸 mtime 的精度在某些文件系统上只有秒级 ⇒ 不睡觉的话前后两次 stat
	// 可能一模一样，测试会"偶发通过" ✗
	time.Sleep(1100 * time.Millisecond)

	got2, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got1.Certificate[0], got2.Certificate[0]) {
		t.Fatal("证书文件换了，握手时却还拿旧的那张 ✗（续期要等重启才生效）")
	}
}

func TestDevCertIsUsable(t *testing.T) {
	cfg, err := tlsconf.NewDev()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("该有一张证书，实得 %d", len(cfg.Certificates))
	}
	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	// 只认 localhost / 127.0.0.1 —— 别让它被误用到线上
	if err := leaf.VerifyHostname("localhost"); err != nil {
		t.Errorf("该认 localhost：%v", err)
	}
	if err := leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("该认 127.0.0.1：%v", err)
	}
	if err := leaf.VerifyHostname("mc.tinyaii.cn"); err == nil {
		t.Error("本地自签证书不该认线上域名 ✗（认了反而危险）")
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Errorf("最低 TLS 版本太低：%x", cfg.MinVersion)
	}
}

// ---- 测试里造自签证书用的小工具 ----

func selfSignedPEM(t *testing.T, cn string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	return certPEM, keyPEM
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// 光看证书本身不够，得**真的握一次手** —— 不然"配置造出来了"和"能用"是两回事。
func TestDevCertHandshakes(t *testing.T) {
	cfg, err := tlsconf.NewDev()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.TLS = cfg
	srv.StartTLS()
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("拿自签证书握手失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 %d", resp.StatusCode)
	}
	if resp.TLS == nil {
		t.Fatal("居然没走 TLS")
	}
	if resp.TLS.Version < tls.VersionTLS12 {
		t.Errorf("TLS 版本太老: %x", resp.TLS.Version)
	}
}
