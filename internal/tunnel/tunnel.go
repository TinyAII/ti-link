// Package tunnel 把打洞成功后的 UDP 通道包装成可靠的字节流（KCP），
// 并在 TCP 连接与它之间双向搬数据 —— 这就是「TCP over UDP」那一层。
package tunnel

import (
	"io"
	"net"

	kcp "github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

// Conv 是 KCP 会话号。一对一连线只开一条会话，写死即可。
const Conv uint32 = 1

// NewSessionWithKey 在已打通的 UDP 通道上建立 KCP 会话，全程 AES-128 加密。
//
// key 由 internal/keyex 协商出来（16 字节）。**不再从口令派生** ——
// 以前是 sha256(固定前缀 + 房间密码)，密码弱、没 KDF，抓到包就能爆破；
// 而且没设密码时会退回到硬编码在客户端里的公共口令，等于没加密。
func NewSessionWithKey(udp net.PacketConn, peer net.Addr, key []byte) (*kcp.UDPSession, error) {
	block, err := kcp.NewAESBlockCrypt(key)
	if err != nil {
		return nil, err
	}
	sess, err := kcp.NewConn3(Conv, peer, block, 0, 0, udp)
	if err != nil {
		return nil, err
	}
	// 游戏流量取向：牺牲一点带宽换低延迟
	sess.SetNoDelay(1, 20, 2, 1)
	sess.SetWindowSize(256, 256)
	sess.SetMtu(1200)
	return sess, nil
}

// Bridge 在 TCP 连接与 KCP 会话之间双向转发，任一方向结束即整体收摊。
func Bridge(tcp net.Conn, sess net.Conn, logf func(string, ...any)) {
	done := make(chan struct{}, 2)
	go func() {
		n, err := io.Copy(sess, tcp)
		logf("转发 TCP→KCP 结束: %d 字节 (%v)", n, err)
		done <- struct{}{}
	}()
	go func() {
		n, err := io.Copy(tcp, sess)
		logf("转发 KCP→TCP 结束: %d 字节 (%v)", n, err)
		done <- struct{}{}
	}()
	<-done
	tcp.Close()
	sess.Close()
}

// MuxClient 在 KCP 会话上叠一层多路复用，本端负责「打开」流。
// 游戏客户端连哪端，哪端就是 Client。
func MuxClient(sess net.Conn) (*smux.Session, error) { return smux.Client(sess, nil) }

// MuxServer 在 KCP 会话上叠一层多路复用，本端负责「接受」流。
// 为什么需要它：MC 客户端连一次服务器会开两条 TCP（先查状态、再真进游戏），
// 单连接转发接不住第二条，必须用多路复用。
func MuxServer(sess net.Conn) (*smux.Session, error) { return smux.Server(sess, nil) }
