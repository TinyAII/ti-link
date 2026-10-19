// Package signal 封装与信令服务器之间的一条 TCP 连接（一行一个 JSON）。
package signal

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"sync"
	"time"

	"p2plink/internal/proto"
)

// maxMsgBytes 单条消息的大小上限。
//
// 🩸 不设限的后果：json.Decoder 会一路吃到内存里，对面发一行几个 G 的
// "JSON" 就能把进程顶爆。协议本来就是一行一个 JSON，用 Scanner 逐行读
// 天然带上限 —— 超了会给 ErrTooLong，不会把内存吃光。
// 我们自己的消息都很小：候选项几条、成员名单几百字节，100 个人也就 8KB 左右。
const maxMsgBytes = 64 << 10

// Client 是一条到信令服务器的连接。Send 是并发的，Recv 由单方调用。
type Client struct {
	conn net.Conn
	enc  *json.Encoder
	sc   *bufio.Scanner
	mu   sync.Mutex
}

// newClient 把连接包成客户端。逐行读 + 单行上限见 maxMsgBytes 的说明。
func newClient(conn net.Conn) *Client {
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 4<<10), maxMsgBytes)
	return &Client{
		conn: conn,
		enc:  json.NewEncoder(conn),
		sc:   sc,
	}
}

// useTLSFor 判断这个地址该不该走 TLS。
//
// 规则：**域名 → TLS，IP → 明文**。
//
// 为什么这么定：本地联调（tmp/e2e_pass.py）起的是 127.0.0.1:xxxx，那里没有证书、
// 也不该有；而正式环境一律用域名。这样"生产走加密"是**默认行为** ——
// 不需要谁记得去开，也不可能有人误把生产配成明文。
//
// 并且**不做降级**：该 TLS 的时候握手失败就让它失败，绝不悄悄退回明文。
// 降级比不加密更坏 —— 它让人以为自己是安全的。
func useTLSFor(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return net.ParseIP(host) == nil
}

// Dial 连上信令服务器。域名走 TLS（校验系统根证书 + 域名），IP 走明文。
func Dial(addr string, timeout time.Duration) (*Client, error) {
	var c net.Conn
	var err error
	if useTLSFor(addr) {
		host, _, _ := net.SplitHostPort(addr)
		d := &net.Dialer{Timeout: timeout}
		c, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		})
	} else {
		c, err = net.DialTimeout("tcp", addr, timeout)
	}
	if err != nil {
		return nil, err
	}
	return newClient(c), nil
}

// New 用一个已有连接构造客户端（信令服务器侧用）。
func New(conn net.Conn) *Client { return newClient(conn) }

// Send 发一条消息。
func (c *Client) Send(m proto.Msg) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(m)
}

// Recv 读一条消息。timeout <= 0 表示不设超时。
// 超长的一行会返回 bufio.ErrTooLong，而不是把内存吃光。
func (c *Client) Recv(timeout time.Duration) (proto.Msg, error) {
	var m proto.Msg
	if timeout > 0 {
		c.conn.SetReadDeadline(time.Now().Add(timeout))
	} else {
		c.conn.SetReadDeadline(time.Time{})
	}
	if !c.sc.Scan() {
		if err := c.sc.Err(); err != nil {
			return m, err
		}
		return m, io.EOF
	}
	err := json.Unmarshal(c.sc.Bytes(), &m)
	return m, err
}

// Close 关掉连接。
func (c *Client) Close() error { return c.conn.Close() }

// CloseWrite 只关"写"那一半：话说完、发 FIN，读的那半边先留着。
//
// 🩸 **为什么不能直接 Close**（2026-10-16 实测踩到）：
// 连接上还躺着**没读完的来数据**时（房主一直在发候选），
// Close 会让内核发 **RST** —— 那刚刚写进去的那句话会被对面直接丢掉 ✗
// 症状是：服务端写了「管理员关闭了这个房间」，房主那边只看到
// "与穿透服务器的连接断开：EOF"。
// 半关走的是 FIN：对面能同时拿到"最后那句话"和"结束了"两件事 ✓
//
// 📌 TCP 支持半关；不是所有 net.Conn 都支持（比如 net.Pipe 就不支持）——
// 不支持就退回整关，行为跟以前一样 ✓
func (c *Client) CloseWrite() error {
	if cw, ok := c.conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}
