// 登录接口客户端的测试。
//
// 这里最要紧的不是"能不能登录成功"（那条路很简单），而是**出错的时候说的是什么话**：
// 用户看到"邮箱或密码不对"会去改密码，看到"连不上服务器"会去看网络 ——
// 把这两种搞混，用户就会在错的地方一直使劲 ✗
package loginapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- 服务器地址的校验 ----

func TestCheckBase(t *testing.T) {
	ok := []struct{ in, want string }{
		{"mc.tinyaii.cn:8444", "https://mc.tinyaii.cn:8444"},
		{"https://mc.tinyaii.cn:8444", "https://mc.tinyaii.cn:8444"},
		{"https://mc.tinyaii.cn:8444/", "https://mc.tinyaii.cn:8444"},
		{"  https://a.com  ", "https://a.com"},
		{"http://127.0.0.1:8443", "http://127.0.0.1:8443"},
		{"http://localhost:8443", "http://localhost:8443"},
		{"http://[::1]:8443", "http://[::1]:8443"},
	}
	for _, c := range ok {
		got, err := CheckBase(c.in)
		if err != nil {
			t.Errorf("CheckBase(%q) 该通过，却被拒：%v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("CheckBase(%q) = %q，该是 %q", c.in, got, c.want)
		}
	}

	bad := []struct{ in, why string }{
		{"", "空的"},
		{"   ", "全是空格"},
		{"ftp://a.com", "不是 http/https"},
		{"https://", "没有主机名"},
		{"https://a.com/some/path", "带了路径（多半是复制错了）"},
		{"https://a.com/api", "带了路径"},
		{"http://mc.tinyaii.cn:8444", "远端的 http —— 密码会明文上网 ✗"},
		{"http://1.2.3.4", "远端 IP 的 http"},
	}
	for _, c := range bad {
		if got, err := CheckBase(c.in); err == nil {
			t.Errorf("CheckBase(%q)（%s）该被拒，却给了 %q ✗", c.in, c.why, got)
		}
	}
}

// ⭐ 这条是上面那张表里最要紧的一条，单独拎出来 —— 它是**安全边界**：
// 非本机地址不许用 http，因为那等于把密码明文发出去。
//
// 反证方法：把 CheckBase 里那个 `u.Scheme == "http" && !isLoopback(...)` 判断删掉，
// 这条立刻红 ✓
func TestCheckBaseRejectsPlainHTTPOffLoopback(t *testing.T) {
	for _, host := range []string{"mc.tinyaii.cn", "1.2.3.4", "192.168.1.5", "example.com:8444"} {
		_, err := CheckBase("http://" + host)
		if err == nil {
			t.Fatalf("http://%s 该被拒（明文发密码）✗", host)
		}
		if !strings.Contains(err.Error(), "https") {
			t.Errorf("拒绝的理由该提到 https，实得：%v", err)
		}
	}
}

// ---- 登录 / 注册 ----

// fakePanel 起一个"假面板"，按 path 回固定的东西。
func fakePanel(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c, err := New(ts.URL) // httptest 给的是 127.0.0.1 ⇒ CheckBase 放行明文 ✓
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoginSuccess(t *testing.T) {
	c := fakePanel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/login" {
			t.Errorf("登录该打 /api/login，实得 %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
			t.Errorf("Content-Type 该是 json，实得 %q", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"tok-123","id":7,"email":"a@qq.com","role":"user"}`))
	})
	a, err := c.Login("a@qq.com", "goodpassword")
	if err != nil {
		t.Fatal(err)
	}
	if a.Token != "tok-123" || a.Email != "a@qq.com" || a.ID != 7 {
		t.Fatalf("拿回来的不对：%+v", a)
	}
}

// 服务器明确说"哪里不对"时，**原话要传下去** —— 那是能直接给用户看的。
func TestServerErrorMessageIsPassedThrough(t *testing.T) {
	c := fakePanel(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"邮箱或密码不对"}`))
	})
	_, err := c.Login("a@qq.com", "wrong")
	if err == nil {
		t.Fatal("该报错")
	}
	var se *ServerErr
	if !errors.As(err, &se) {
		t.Fatalf("该是 ServerErr，实得 %T", err)
	}
	if se.Msg != "邮箱或密码不对" || se.Status != 401 {
		t.Fatalf("原话没传下来：%+v", se)
	}
}

// 200 但回的不是 JSON（多半是地址指到了个网页）⇒ 得说"检查地址"，
// 而不是让人以为是密码错了 ✗
func TestNonJSONResponse(t *testing.T) {
	c := fakePanel(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>这是个网页</html>"))
	})
	_, err := c.Login("a@qq.com", "x")
	if err == nil || !strings.Contains(err.Error(), "地址") {
		t.Fatalf("该提示检查服务器地址，实得：%v", err)
	}
}

// 200 但没有 token ⇒ 不能当成功放过去（不然本地会存个空令牌）
func TestMissingTokenIsAnError(t *testing.T) {
	c := fakePanel(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":1,"email":"a@qq.com"}`))
	})
	if _, err := c.Login("a@qq.com", "x"); err == nil {
		t.Fatal("服务器没给令牌却当成功了 ✗")
	}
}

// 连不上：**要说"连不上"，不许说"密码错"**。
func TestUnreachable(t *testing.T) {
	// 127.0.0.1 上挑一个没人听的端口
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close() // 关掉，端口就空出来了

	c, err := New(url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Login("a@qq.com", "x")
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("该报 ErrUnreachable，实得 %v", err)
	}
	if strings.Contains(err.Error(), "密码") {
		t.Errorf("连不上却说密码的事 —— 这会把用户带偏 ✗：%v", err)
	}
}

// 超时：说"没响应"，也别拖太久。
func TestTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	t.Cleanup(ts.Close)

	c, err := New(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.hc.Timeout = 150 * time.Millisecond // 测试里不用等满 10 秒
	_, err = c.Login("a@qq.com", "x")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("该报 ErrTimeout，实得 %v", err)
	}
}

// 服务器要求跳转 ⇒ 拒绝（跟着跳等于把密码递给第三方）
func TestRedirectRefused(t *testing.T) {
	var other *httptest.Server
	other = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"token":"stolen"}`))
	}))
	t.Cleanup(other.Close)

	c := fakePanel(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/login", http.StatusFound)
	})
	if _, err := c.Login("a@qq.com", "x"); err == nil {
		t.Fatal("跟着跳转了 —— 那等于把密码递给第三方 ✗")
	}
}

// 响应体超大：要拦住，别把内存吃光（而且要看得出是"这服务器不对劲"）
func TestHugeResponseIsTruncated(t *testing.T) {
	c := fakePanel(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"token":"` + strings.Repeat("A", 1<<20) + `"}`))
	})
	_, err := c.Login("a@qq.com", "x")
	if err == nil {
		t.Fatal("超大响应该被截成解析失败，而不是当成功 ✗")
	}
}

func TestSignUp(t *testing.T) {
	var gotPath string
	var gotBody string
	c := fakePanel(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		_, _ = w.Write([]byte(`{"token":"t","id":2,"email":"b@qq.com","role":"user"}`))
	})
	a, err := c.SignUp("ABCDE-FGHJK", "b@qq.com", "goodpassword", "123456")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/signup" {
		t.Errorf("注册该打 /api/signup，实得 %s", gotPath)
	}
	if a.Email != "b@qq.com" {
		t.Errorf("邮箱不对：%s", a.Email)
	}
	// 邀请码要原样发过去（规范化是服务器那边的事，客户端别自作聪明改它）
	if !strings.Contains(gotBody, "ABCDE-FGHJK") {
		t.Errorf("请求体里没带上原样的邀请码：%s", gotBody)
	}
}

func TestMeCarriesBearerToken(t *testing.T) {
	c := fakePanel(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-x" {
			t.Errorf("该带 Bearer 令牌，实得 %q", got)
		}
		_, _ = w.Write([]byte(`{"id":1,"email":"a@qq.com","role":"user"}`))
	})
	a, err := c.Me("tok-x")
	if err != nil {
		t.Fatal(err)
	}
	if a.Email != "a@qq.com" {
		t.Errorf("邮箱不对：%s", a.Email)
	}
}

// 注销：服务器那边 500 也要让调用方知道（本地清不清令牌是调用方的决定）
func TestLogoutReportsServerFailure(t *testing.T) {
	c := fakePanel(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"服务器内部错误"}`))
	})
	if err := c.Logout("tok"); err == nil {
		t.Fatal("服务器出错该报出来（本地令牌清不清由调用方决定）")
	}
}
