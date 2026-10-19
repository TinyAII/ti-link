// 登录校验这道门（**唯一算数的鉴权点**）的测试。
//
// 单独一个文件、单独一套装置，理由：其余那些房间测试验的是"房间逻辑"，
// 它们跑在 `-insecure-no-auth` 下（见 startTestServer 的注释）。
// 混在一起测的话，门一坏，报错会以"等 room_ok 读失败"的形式出现 ——
// 看着像房间逻辑坏了，其实不是 ✗
//
// 🩸 这里用的是**真账号库**（一个临时目录、真发码、真注册），
// 不是造一个假的校验函数 —— 因为要验的恰恰是"信令进程能不能读到面板写的账号"。
package main

import (
	"bytes"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"p2plink/internal/auth"
	"p2plink/internal/proto"
	"p2plink/internal/signal"
)

// authServer 是一套"开着登录校验"的装置。
type authServer struct {
	addr   string
	token  string
	acctID int64
	// writer 是**可写**的那份句柄（扮演管理面板）。
	// 服务器手里那份是只读的，靠 mtime 发现它改了东西 ✓
	writer *auth.Store
	dir    string
}

func startAuthServer(t *testing.T) *authServer {
	t.Helper()
	dir := t.TempDir()

	// ① 面板侧：发码 + 注册一个账号
	w, err := auth.Open(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	codes, err := w.GenCodes(1, "test", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a, tok, err := w.SignUp(codes[0], "a@qq.com", "goodpassword", auth.DefaultEmailDomains, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// ② 服务器侧：**只读**打开同一份账号库 —— 跟真部署里的分工一模一样
	ro, err := auth.Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	oldSt, oldNoAuth := st, insecureNoAuth
	st, insecureNoAuth = ro, false
	t.Cleanup(func() { st, insecureNoAuth = oldSt, oldNoAuth })

	// ③ 起监听器
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起测试监听器失败: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()

	return &authServer{addr: ln.Addr().String(), token: tok, acctID: a.ID, writer: w, dir: dir}
}

// register 发一条 host 注册。token 空就是"没带"。
func (s *authServer) register(t *testing.T, c *signal.Client, room, token string) {
	t.Helper()
	send(t, c, proto.Msg{
		Type: proto.TypeRegister, Role: "host", Game: "我的世界", Room: room, Token: token,
		ClientID: "cid-" + room, Label: nickA, MaxSize: 4,
		PubKey: newPub(t),
	})
}

// ---- ① 没带令牌 ----

func TestNoTokenIsRejected(t *testing.T) {
	s := startAuthServer(t)
	before := statNoToken.Load()

	c := dialT(t, s.addr)
	s.register(t, c, "T-NOAUTH", "")
	m := recvUntil(t, c, proto.TypeError)

	if !strings.Contains(m.Err, "登录") {
		t.Errorf("没带令牌该提示「登录」，实得 %q", m.Err)
	}
	// 🩸 话得说**能让用户动起来**：他多半只是没登录，
	// 所以不能只说"拒绝"，要告诉他去登录 ✓
	if !strings.Contains(m.Err, "客户端") {
		t.Errorf("该告诉他去哪儿登录，实得 %q", m.Err)
	}
	if statNoToken.Load() <= before {
		t.Error("没记上「没带令牌」这一笔 —— 运维时看不出有人在硬连")
	}
}

// ---- ② 令牌不认 ----

func TestBogusTokenIsRejected(t *testing.T) {
	s := startAuthServer(t)
	before := statBadToken.Load()

	for _, bad := range []string{"garbage", s.token + "x", strings.Repeat("A", 43)} {
		c := dialT(t, s.addr)
		s.register(t, c, "T-BADTOK", bad)
		m := recvUntil(t, c, proto.TypeError)
		if !strings.Contains(m.Err, "登录") {
			t.Errorf("令牌 %q 不认时该提示「登录」，实得 %q", bad, m.Err)
		}
		c.Close()
	}
	if statBadToken.Load() <= before {
		t.Error("没记上「令牌不认」这一笔")
	}
}

// ---- ③ 有效令牌能正常建房 ----

func TestValidTokenCreatesRoom(t *testing.T) {
	s := startAuthServer(t)
	c := dialT(t, s.addr)
	s.register(t, c, "T-OKTOK", s.token)
	if m := recvUntil(t, c, proto.TypeRoomOK); m.Room != "T-OKTOK" {
		t.Fatalf("拿有效令牌该能建房，实得 %+v", m)
	}
}

// ⭐⭐ 停用账号要**立刻**生效 —— 这条验的是"信令侧真的在重读账号库"。
//
// 面板把账号一停，服务器那边**下一个连接**就该进不来。
// 不重读的话，这个改动要等到信令服务重启才生效 —— 而它可能几周都不重启，
// 表现是"后台明明显示已停用，人却照样能连"，最容易被当成界面 Bug ✗
func TestDisableTakesEffectImmediately(t *testing.T) {
	s := startAuthServer(t)

	// 先确认能用
	c := dialT(t, s.addr)
	s.register(t, c, "T-DIS-1", s.token)
	recvUntil(t, c, proto.TypeRoomOK)

	// 面板把它停了
	if err := s.writer.SetStatus(s.acctID, auth.StatusDisabled, time.Now()); err != nil {
		t.Fatal(err)
	}

	// 同一个令牌，下一个连接就该进不来
	c2 := dialT(t, s.addr)
	s.register(t, c2, "T-DIS-2", s.token)
	if m := recvUntil(t, c2, proto.TypeError); !strings.Contains(m.Err, "登录") {
		t.Fatalf("账号已停用，这个令牌该立刻失效，实得 %q ✗", m.Err)
	}
}

// 强制下线同理。
func TestRevokeTakesEffectImmediately(t *testing.T) {
	s := startAuthServer(t)
	c := dialT(t, s.addr)
	s.register(t, c, "T-REV-1", s.token)
	recvUntil(t, c, proto.TypeRoomOK)

	if err := s.writer.RevokeSessions(s.acctID, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	c2 := dialT(t, s.addr)
	s.register(t, c2, "T-REV-2", s.token)
	if m := recvUntil(t, c2, proto.TypeError); !strings.Contains(m.Err, "登录") {
		t.Fatalf("强制下线之后这个令牌该失效，实得 %q ✗", m.Err)
	}
}

// ---- ④ -insecure-no-auth 是唯一的例外 ----

func TestInsecureNoAuthSkipsCheck(t *testing.T) {
	s := startAuthServer(t)

	// 开着校验时进不去
	c := dialT(t, s.addr)
	s.register(t, c, "T-INS-1", "")
	recvUntil(t, c, proto.TypeError)
	c.Close()

	// 挂上"不安全开关"（本地测试用），同样的请求就通了
	insecureNoAuth = true
	defer func() { insecureNoAuth = false }()

	c2 := dialT(t, s.addr)
	s.register(t, c2, "T-INS-2", "")
	if m := recvUntil(t, c2, proto.TypeRoomOK); m.Room != "T-INS-2" {
		t.Fatalf("-insecure-no-auth 下该能免登录建房，实得 %+v", m)
	}
}

// ---- ⑤ 令牌不许进日志 ----

// 🩸 令牌是凭据。日志会被别人看到（也可能被打包发出去），
// 它进去了就等于把"能直接连服务器的钥匙"贴在墙上 ✗
// （跟"日志里地址要打码"是同一族规矩，见 masklog_test.go。）
func TestTokenNeverLogged(t *testing.T) {
	s := startAuthServer(t)

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	// 一条成功的 + 一条失败的，两条路都走一遍
	c := dialT(t, s.addr)
	s.register(t, c, "T-LOG-1", s.token)
	recvUntil(t, c, proto.TypeRoomOK)
	c.Close()

	c2 := dialT(t, s.addr)
	s.register(t, c2, "T-LOG-2", "")
	recvUntil(t, c2, proto.TypeError)
	c2.Close()

	logged := buf.String()
	if logged == "" {
		t.Fatal("一条日志都没有 —— 那是日志没了，不是安全了")
	}
	if strings.Contains(logged, s.token) {
		t.Errorf("日志里出现了登录令牌 ✗\n%s", logged)
	}
	// 连前缀都不该露 —— 令牌被截一半照样是泄露
	if len(s.token) > 8 && strings.Contains(logged, s.token[:8]) {
		t.Errorf("日志里出现了令牌的前几位 ✗\n%s", logged)
	}
}

// ---- ⑥ 顺序：先校验登录，再说别的 ----

// 没登录的人不该从服务器这儿问出"房间在不在""密码对不对"这些信息。
// 这条钉住的是 handle() 里的**次序**（登录校验在 validateRegister 和角色分发之前）。
func TestAuthCheckedBeforeAnythingElse(t *testing.T) {
	s := startAuthServer(t)
	c := dialT(t, s.addr)
	// 故意给一堆乱七八糟的房间名 —— 如果先做字段校验，报的会是"房间 ID 不合法"
	send(t, c, proto.Msg{
		Type: proto.TypeRegister, Role: "join", Game: "我的世界", Room: "!!!非法!!!", Token: "",
		ClientID: "cid-x", Cands: []string{"10.0.0.1:1"},
	})
	m := recvUntil(t, c, proto.TypeError)
	if !strings.Contains(m.Err, "登录") {
		t.Fatalf("该先报「登录」，实得 %q —— 是不是把次序调反了？", m.Err)
	}
	if strings.Contains(m.Err, "房间") {
		t.Errorf("还没登录就把房间相关的话说出来了：%q", m.Err)
	}
}
