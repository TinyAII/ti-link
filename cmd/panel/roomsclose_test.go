// 「从后台关房间」这条通路的测试。
//
// 🩸 这次最容易**悄悄坏**的就是这条：信令侧的本机端点从 TCP 换成了 Unix socket，
// 面板那边得跟着改 Transport —— 改动一漏，症状只是"点了关闭没反应"，
// 而两边各自的测试都还是绿的 ✗
// 所以这条测试**两端都接真的**：面板 → 真 socket → 假信令看它收到了什么 ✓
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"p2plink/internal/auth"
)

// fakeSignal 是个走**真 Unix socket** 的假信令，只实现 /rooms/close。
type fakeSignal struct {
	sock  string
	mu    sync.Mutex
	got   []string // 每次收到请求，把 body 记下来
	reply map[string]string
}

func startFakeSignal(t *testing.T) *fakeSignal {
	t.Helper()
	// 🩸 socket 路径要**短**：Windows 上 AF_UNIX 的路径上限约 108 字节，
	// 而 t.TempDir() 会把"测试函数名 + 随机数"拼进路径 ⇒ 一超就 bind 失败 ✗
	// 症状是 `bind: invalid argument`，看着完全不像"名字太长"（2026-10-16 踩到）
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("p2psig%d.sock", rand.Int63n(1e9)))
	t.Cleanup(func() { _ = os.Remove(sock) })

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSignal{sock: sock, reply: map[string]string{}}
	srv := &http.Server{Handler: http.HandlerFunc(f.serve)}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f
}

func (f *fakeSignal) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.got = append(f.got, r.Method+" "+r.URL.Path+" "+string(body))
	room := ""
	var req struct {
		Room string `json:"room"`
	}
	if json.Unmarshal(body, &req) == nil {
		room = req.Room
	}
	msg, refused := f.reply[room]
	f.mu.Unlock()

	if refused {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (f *fakeSignal) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

// newTestWithSignal 跟 newTest 一样，只是把信令侧的 socket 路径接上。
func newTestWithSignal(t *testing.T, sock string) (*httptest.Server, *Server, *auth.Store) {
	t.Helper()
	st, err := auth.Open(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(st, auth.DefaultEmailDomains, []byte("test-csrf-key-0123456789abcdef"), sock)
	ts := httptest.NewTLSServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv, st
}

// ⭐⭐ 面板点「关闭」⇒ 请求真的**穿过那个 Unix socket** 到了信令侧，
// 而且带上了正确的房间号。
//
// 反证方法：把 server.go 里那个 Transport 的 DialContext 删掉（退回默认 TCP）⇒
// 立刻红 ✓（默认 Transport 会去连主机名 signal，根本连不上）
func TestAdminCloseRoomReachesSignalOverSocket(t *testing.T) {
	f := startFakeSignal(t)
	ts, _, st := newTestWithSignal(t, f.sock)
	c, csrf := adminLogin(t, ts, st)

	code, out, _ := do(t, c, "POST", ts.URL+"/api/admin/rooms/close",
		map[string]string{"room": "TIabcdef"}, map[string]string{headerCSRF: csrf})
	if code != http.StatusOK {
		t.Fatalf("该关成功，实得 %d %v", code, out)
	}
	seen := f.seen()
	if len(seen) != 1 {
		t.Fatalf("假信令该收到 1 个请求，实得 %d 个：%v", len(seen), seen)
	}
	if !strings.Contains(seen[0], "POST /rooms/close") {
		t.Errorf("路径/方法不对：%s", seen[0])
	}
	if !strings.Contains(seen[0], "TIabcdef") {
		t.Errorf("房间号没带过去：%s", seen[0])
	}
}

// 信令侧说"没有这个房间" ⇒ 那句话要**原样**回到前端（前端直接 alert 它）
func TestAdminCloseRoomRelaysRefusal(t *testing.T) {
	f := startFakeSignal(t)
	f.reply["TIgone"] = "没有这个房间（可能刚被关掉了）"
	ts, _, st := newTestWithSignal(t, f.sock)
	c, csrf := adminLogin(t, ts, st)

	code, out, _ := do(t, c, "POST", ts.URL+"/api/admin/rooms/close",
		map[string]string{"room": "TIgone"}, map[string]string{headerCSRF: csrf})
	if code != http.StatusConflict {
		t.Fatalf("该原样透传 409，实得 %d %v", code, out)
	}
	if s, _ := out["error"].(string); !strings.Contains(s, "没有这个房间") {
		t.Errorf("该把信令侧那句话转给前端，实得 %q", s)
	}
}

// 空房间号：不该往后传（省一次没意义的本机调用）
func TestAdminCloseRoomNeedsRoom(t *testing.T) {
	f := startFakeSignal(t)
	ts, _, st := newTestWithSignal(t, f.sock)
	c, csrf := adminLogin(t, ts, st)

	code, _, _ := do(t, c, "POST", ts.URL+"/api/admin/rooms/close",
		map[string]string{}, map[string]string{headerCSRF: csrf})
	if code != http.StatusBadRequest {
		t.Fatalf("该回 400，实得 %d", code)
	}
	if n := len(f.seen()); n != 0 {
		t.Fatalf("空房间号不该往信令那边传，实得 %d 个请求", n)
	}
}

// socket 不存在（信令没跑 / 没配 -local）⇒ 502 + 一句人话
func TestAdminCloseRoomWhenSignalDown(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "根本没有这个.sock")
	ts, _, st := newTestWithSignal(t, missing)
	c, csrf := adminLogin(t, ts, st)

	code, out, _ := do(t, c, "POST", ts.URL+"/api/admin/rooms/close",
		map[string]string{"room": "TIabcd"}, map[string]string{headerCSRF: csrf})
	if code != http.StatusBadGateway {
		t.Fatalf("信令侧没接上该回 502，实得 %d %v", code, out)
	}
	if s, _ := out["error"].(string); !strings.Contains(s, "没接上") {
		t.Errorf("该说清是没接上，实得 %q", s)
	}
}

// 完全没配信令（-signal 没给）⇒ 503，而且要说清是"没配"
func TestAdminCloseRoomNotConfigured(t *testing.T) {
	ts, _, st := newTestWithSignal(t, "")
	c, csrf := adminLogin(t, ts, st)

	code, out, _ := do(t, c, "POST", ts.URL+"/api/admin/rooms/close",
		map[string]string{"room": "TIabcd"}, map[string]string{headerCSRF: csrf})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("没配该回 503，实得 %d %v", code, out)
	}
	if s, _ := out["error"].(string); !strings.Contains(s, "-signal") {
		t.Errorf("该提示启动参数，实得 %q", s)
	}
}

// 关房间必须**要登录** —— 前端把按钮藏起来不算防线，判定得在服务端 ✗
func TestAdminCloseRoomNeedsAuth(t *testing.T) {
	f := startFakeSignal(t)
	ts, _, _ := newTestWithSignal(t, f.sock)

	// 没登录、也没带 CSRF
	code, _, _ := do(t, ts.Client(), "POST", ts.URL+"/api/admin/rooms/close",
		map[string]string{"room": "TIabcd"}, nil)
	if code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("没登录该被挡（401/403），实得 %d ✗", code)
	}
	if n := len(f.seen()); n != 0 {
		t.Fatalf("没登录的请求不该摸到信令侧，实得 %d 个", n)
	}
}
