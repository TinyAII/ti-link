// 本机端点（Unix socket）的测试。
//
// 🩸 最要紧的一条不是"接口能不能用"，而是 **socket 文件是不是 0600** ★★
// 那个权限**就是**这套东西的鉴权：漏了它，同机任何用户都能连上、
// 然后 POST 一下把别人正在玩的房间全踢掉 ✗
// （这正是当初把 TCP 127.0.0.1 换成 socket 的原因 —— 见 localapi.go 顶上那段）
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"p2plink/internal/proto"
	"p2plink/internal/signal"
)

// startLocal 起一个真的本机端点（临时目录里的 socket），返回 socket 路径。
//
// ⚠️ 走 newLocalAPI + 自己 go Serve，**不走 serveLocalAPI** ——
// 后者内部那句 Serve 是阻塞的，直接调会挂死 ✗（踩过一次：挂满六分钟）
func startLocal(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "local.sock")
	srv, ln, err := newLocalAPI(sock)
	if err != nil {
		t.Fatalf("端点没建起来: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	// 等 socket 文件出现（Serve 是异步的）
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			return sock
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("socket 文件一直没出现")
	return ""
}

// localClient 是一个**只能连那个 socket** 的 http.Client。
func localClient(sock string) *http.Client {
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
}

// ★★ 这条是这套东西的安全地基：socket 必须是 0600。
//
// 反证方法：把 localapi.go 里那个 os.Chmod(sockPath, 0o600) 删掉 ⇒ 立刻红 ✓
//
// ⚠️ **只在 Linux 上有效**：Windows 没有 POSIX 权限位（os.Chmod 只切只读位），
// 那儿的 socket 一律报 0666 ⇒ 在这儿跑这条只会得出一个假结论 ✗
// 所以本机（Windows）跳过，**真正的验证在部署那台 Linux 上**：
// 部署脚本启动之后会 stat 一下那个 socket，不是 600 就当场报错 ✓
func TestLocalSocketIs0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 没有 POSIX 权限位 —— 这条由部署脚本在 Linux 上真验一次")
	}
	sock := startLocal(t)
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket 权限该是 0600，实得 %04o ✗ "+
			"（松了的话同机任何用户都能连上，然后 POST 一下把所有房间踢掉）", got)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("这该是个 socket 文件，实得 %v", fi.Mode())
	}
}

// 读的那两个路径还能用（换 socket 不该把它们弄坏）
func TestLocalReadPathsStillWork(t *testing.T) {
	sock := startLocal(t)
	c := localClient(sock)
	for _, p := range []string{"/stats", "/rooms"} {
		resp, err := c.Get("http://signal" + p)
		if err != nil {
			t.Fatalf("GET %s 失败: %v", p, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s 该 200，实得 %d", p, resp.StatusCode)
		}
		if !json.Valid(body) {
			t.Errorf("GET %s 回的不是合法 JSON: %s", p, body)
		}
	}
}

// 关房间只接受 POST —— GET 不该能改任何东西
func TestCloseRoomRejectsGET(t *testing.T) {
	sock := startLocal(t)
	resp, err := localClient(sock).Get("http://signal/rooms/close")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /rooms/close 该 405，实得 %d ✗", resp.StatusCode)
	}
}

func postClose(t *testing.T, sock, room string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"room": room})
	resp, err := localClient(sock).Post("http://signal/rooms/close", "application/json",
		strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func TestCloseRoomUnknown(t *testing.T) {
	sock := startLocal(t)
	code, out := postClose(t, sock, "TI根本没有这个房")
	if code != http.StatusConflict {
		t.Fatalf("没有的房间该回 409，实得 %d %s", code, out)
	}
	if !strings.Contains(out, "没有这个房间") {
		t.Errorf("该说清「没有这个房间」，实得 %s", out)
	}
	// 空房间号：该被挡在最外面
	if code, _ := postClose(t, sock, ""); code != http.StatusBadRequest {
		t.Fatalf("空房间号该回 400，实得 %d", code)
	}
}

// 房主已经不在了（表里有房间但 host 是 nil）⇒ 给一句人话，别崩
func TestCloseRoomWithoutHost(t *testing.T) {
	sock := startLocal(t)
	r := mkRoom("TInohost", "1.1.1.1", 4, "", 0, time.Minute)
	r.host = nil
	roomsMu.Lock()
	rooms["TInohost"] = r
	roomsMu.Unlock()
	t.Cleanup(func() { roomsMu.Lock(); delete(rooms, "TInohost"); roomsMu.Unlock() })

	code, out := postClose(t, sock, "TInohost")
	if code != http.StatusConflict {
		t.Fatalf("该回 409，实得 %d %s", code, out)
	}
	if !strings.Contains(out, "房主") {
		t.Errorf("该说清是房主不在了，实得 %s", out)
	}
}

// ⭐⭐ 关键机制：关房间 = **把房主的连接关掉**（收尾交给现有那条清理路径）。
//
// 反证方法：把 closeRoomByAdmin 里那句 host.cl.Close() 删掉 ⇒ 立刻红 ✓
// 红了意味着：面板点了「关闭」，房间还在、人还连着，什么都没发生 ✗
func TestCloseRoomClosesHostConn(t *testing.T) {
	sock := startLocal(t)

	// 用一对管道假装"房主那条连接"：一端给 Client，另一端我们拿着看它有没有被关
	hostSide, srvSide := net.Pipe()
	defer hostSide.Close()
	cl := signal.New(srvSide)

	r := mkRoom("TIcloseme", "2.2.2.2", 4, "某个房主", 0, time.Minute)
	r.host.cl = cl
	roomsMu.Lock()
	rooms["TIcloseme"] = r
	roomsMu.Unlock()
	t.Cleanup(func() { roomsMu.Lock(); delete(rooms, "TIcloseme"); roomsMu.Unlock() })

	// 🩸 **必须后台并发地读**：net.Pipe 是**无缓冲**的 ——
	// 没人读的时候，信令那边那句 Send 会一直堵着，然后触发出 3 秒超时 ✗
	// （真实的 TCP 连接有发送缓冲，不会这样；这是测试脚手架的脾气，不是产品的问题）
	type readResult struct {
		msg proto.Msg
		err error
	}
	readCl := signal.New(hostSide)
	ch := make(chan readResult, 1)
	go func() {
		m, err := readCl.Recv(3 * time.Second)
		ch <- readResult{m, err}
	}()

	code, out := postClose(t, sock, "TIcloseme")
	if code != http.StatusOK {
		t.Fatalf("该关成功，实得 %d %s", code, out)
	}

	// ① 房主那端该**先收到一句人话**，然后才是连接断掉。
	//
	// 🩸 次序和文案都要对：不先说一句的话，房主看到的是
	// "与穿透服务器的连接断了，检查一下网络" ⇒ 他会去查自己的网、然后重试，
	// 而其实是被管理员关了 ✗（2026-10-16 实测就是这个症状）
	g := <-ch
	if g.err != nil {
		t.Fatalf("该先收到一条「管理员关闭了」的通知，实得错误 %v ✗", g.err)
	}
	if g.msg.Type != proto.TypeHostGone {
		t.Errorf("该是 TypeHostGone（客户端那两个循环只认它），实得 %q", g.msg.Type)
	}
	if !strings.Contains(g.msg.Notice, "管理员") {
		t.Errorf("提示该说清是管理员关的，实得 %q ✗", g.msg.Notice)
	}

	// ② 然后连接该**真的被关掉**（不然面板点了等于没点）。
	//
	// 🩸 这里必须分清"被关掉(EOF)"和"读超时" —— 两个都返回 error ✗
	// 只判 `err != nil` 的话，把 Close 那行删掉之后读会超时 ⇒ 断言**假通过** ✗
	// （2026-10-16 反证时抓到的：那条反证当时报"测试还是绿的"）
	if _, err := readCl.Recv(2 * time.Second); err == nil {
		t.Fatal("房主的连接没被关掉 —— 面板点了关闭等于没点 ✗")
	} else if !errors.Is(err, io.EOF) {
		t.Fatalf("该是因为连接被关掉（EOF），实得 %v —— "+
			"超时的话说明连接还开着 ✗", err)
	}

	// ② 理由该被写上：房里的人看到的是"管理员关闭了这个房间"，
	//    跟"房主自己断了"分开（不分开的话房主会一脸懵："我没关啊"）
	roomsMu.Lock()
	got := rooms["TIcloseme"]
	roomsMu.Unlock()
	if got == nil {
		t.Fatal("房间不该在这时候就没（真正的清理是房主断开那条路做的）")
	}
	got.mu.Lock()
	reason := got.closeReason
	got.mu.Unlock()
	if !strings.Contains(reason, "管理员") {
		t.Fatalf("closeReason 该是「管理员关闭了这个房间」，实得 %q ✗", reason)
	}
}

// 房间里那条"给成员的通知"到底用的是哪句话 —— 直接盯着清理那段用的变量。
//
// 📌 为什么要单独钉：serveHost 那条清理路要跑起来得走一遍完整握手，
// 很重；但"用哪句话"这件事可以在这儿单独钉住 ✓
// （⚠️ 这里**没测到**"整条清理跑完"，那个只有真跑一次 serveHost 才验得了，
//
//	见下面的说明）
func TestCloseReasonDefaultsToHostGone(t *testing.T) {
	r := mkRoom("TIplain", "3.3.3.3", 4, "某房主", 0, time.Minute)
	// 房主自己断的（closeReason 空）⇒ 该用默认那句
	r.mu.Lock()
	notice := r.closeReason
	r.mu.Unlock()
	if notice != "" {
		t.Fatalf("房主自己断的那种，closeReason 该是空的（清理那边会兜成「房主已断开连接」），实得 %q", notice)
	}
	// 而 TypeHostGone 这个类型本身没变
	if proto.TypeHostGone == "" {
		t.Fatal("TypeHostGone 不该是空串")
	}
}
