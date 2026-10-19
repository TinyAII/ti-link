// 登录那几个页面接口的测试。
//
// 这里钉的是**「本地状态什么时候该变、什么时候不该变」** ——
// 那是这一层唯一容易错的地方：
//
//	登录成功   ⇒ 存令牌
//	登录失败   ⇒ 什么都不动
//	服务器说令牌不认（401）⇒ 清掉本地令牌（留着没用了）
//	**只是连不上**        ⇒ 千万别清！网断一下就把人登出，下次还得重输密码 ✗
//
// 最后那两条长得像，后果差很远，所以各有一条测试。
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"p2plink/internal/appdir"
	"p2plink/internal/identity"
)

// setupAuth 把数据目录顶掉、设置缓存清掉、令牌清掉。
func setupAuth(t *testing.T) {
	t.Helper()
	if appdir.IsCustom() {
		t.Skip("数据目录被用户改到别处了 —— 这测试会往那儿写文件，跳过")
	}
	t.Setenv(appdir.EnvDir, t.TempDir())
	resetSettingsCache(t)
	_ = identity.ClearToken()
}

func resetSettingsCache(t *testing.T) {
	t.Helper()
	settingsMu.Lock()
	settingsC = nil
	settingsMu.Unlock()
	t.Cleanup(func() {
		settingsMu.Lock()
		settingsC = nil
		settingsMu.Unlock()
	})
}

// pointAt 让"登录服务器"指向一个假的（httptest 给的是 127.0.0.1 ⇒ 明文放行 ✓）
func pointAt(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	if err := updateSettings(func(s *Settings) { s.LoginBase = ts.URL }); err != nil {
		t.Fatal(err)
	}
	return ts
}

// pointAtDead 指向一个**没人听**的端口（服务先起后关，端口就空出来了）
func pointAtDead(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	if err := updateSettings(func(s *Settings) { s.LoginBase = url }); err != nil {
		t.Fatal(err)
	}
	return url
}

func okPanel(token, email string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"` + token + `","id":1,"email":"` + email + `","role":"user"}`))
	}
}

// ---- AccountState：只看本地，不发网络 ----

func TestAccountStateReflectsLocalToken(t *testing.T) {
	setupAuth(t)
	b := &backend{}

	if st := b.AccountState(); st["loggedIn"] != false {
		t.Fatalf("没令牌时该说没登录，实得 %v", st["loggedIn"])
	}
	if err := identity.SetToken("tok"); err != nil {
		t.Fatal(err)
	}
	if st := b.AccountState(); st["loggedIn"] != true {
		t.Fatalf("有令牌时该说登录着，实得 %v", st["loggedIn"])
	}
	// 服务器地址也要给出来（登录页底部那行小字要用）
	if st := b.AccountState(); st["server"] == "" {
		t.Fatal("该把服务器地址带出来")
	}
}

// ---- 登录：成功存令牌、失败什么都不动 ----

func TestLoginStoresToken(t *testing.T) {
	setupAuth(t)
	pointAt(t, okPanel("tok-abc", "a@qq.com"))
	b := &backend{}

	r := b.Login("a@qq.com", "goodpassword")
	if r["ok"] != true {
		t.Fatalf("该成功，实得 %v", r)
	}
	if got := identity.Token(); got != "tok-abc" {
		t.Fatalf("令牌没存下来，实得 %q", got)
	}
}

func TestLoginFailureChangesNothing(t *testing.T) {
	setupAuth(t)
	pointAt(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"邮箱或密码不对"}`))
	})
	// 先塞一个旧令牌，看失败会不会把它祸害了
	if err := identity.SetToken("old-token"); err != nil {
		t.Fatal(err)
	}
	b := &backend{}

	r := b.Login("a@qq.com", "wrong")
	if r["ok"] != false {
		t.Fatalf("该失败，实得 %v", r)
	}
	// 失败文案要用服务器给的原话
	if s, _ := r["error"].(string); !strings.Contains(s, "密码") {
		t.Errorf("错误文案该把服务器的话带出来，实得 %q", s)
	}
	if got := identity.Token(); got != "old-token" {
		t.Fatalf("登录失败不该动本地令牌，实得 %q ✗", got)
	}
}

func TestSignUpStoresToken(t *testing.T) {
	setupAuth(t)
	pointAt(t, okPanel("tok-new", "b@qq.com"))
	b := &backend{}

	r := b.SignUp("ABCDE-FGHJK", "b@qq.com", "goodpassword", "123456")
	if r["ok"] != true {
		t.Fatalf("该成功，实得 %v", r)
	}
	if got := identity.Token(); got != "tok-new" {
		t.Fatalf("令牌没存下来，实得 %q", got)
	}
}

// ---- 注销：服务器那边成不成功，本地都必须清掉 ----

func TestLogoutClearsTokenEvenIfServerIsDown(t *testing.T) {
	setupAuth(t)
	pointAtDead(t)
	if err := identity.SetToken("tok"); err != nil {
		t.Fatal(err)
	}
	b := &backend{}

	r := b.Logout()
	if r["ok"] != true {
		t.Fatalf("服务器没响应也得算退出成功（否则用户以为退不出去）✗ 实得 %v", r)
	}
	if got := identity.Token(); got != "" {
		t.Fatalf("本地令牌没清掉，实得 %q ✗", got)
	}
}

// ---- ⭐ 验令牌：401 要清、连不上不许清 ----

func TestVerifyAccountExpiredClearsToken(t *testing.T) {
	setupAuth(t)
	pointAt(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"登录已过期，请重新登录"}`))
	})
	if err := identity.SetToken("stale"); err != nil {
		t.Fatal(err)
	}
	b := &backend{}

	r := b.VerifyAccount()
	if r["expired"] != true {
		t.Fatalf("401 该判成 expired，实得 %v", r)
	}
	if got := identity.Token(); got != "" {
		t.Fatalf("服务器说令牌不认了，本地该清掉，实得 %q ✗", got)
	}
}

// 这条跟上面那条**只差一个字**（连不上 vs 令牌不认），后果差很远：
// 网抖一下就把人登出，他下次还得重新输密码 —— 纯属添乱 ✗
func TestVerifyAccountNetworkFailureKeepsToken(t *testing.T) {
	setupAuth(t)
	pointAtDead(t)
	if err := identity.SetToken("still-good"); err != nil {
		t.Fatal(err)
	}
	b := &backend{}

	r := b.VerifyAccount()
	if r["ok"] != false {
		t.Fatalf("连不上该报 ok=false，实得 %v", r)
	}
	if r["expired"] == true {
		t.Fatalf("连不上**不算过期** ✗ 实得 %v", r)
	}
	if got := identity.Token(); got != "still-good" {
		t.Fatalf("连不上就把人登出了，实得 %q ✗", got)
	}
}

func TestVerifyAccountOKReturnsEmail(t *testing.T) {
	setupAuth(t)
	pointAt(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":1,"email":"a@qq.com","role":"user"}`))
	})
	if err := identity.SetToken("tok"); err != nil {
		t.Fatal(err)
	}
	b := &backend{}

	r := b.VerifyAccount()
	if r["ok"] != true || r["email"] != "a@qq.com" {
		t.Fatalf("该拿到邮箱，实得 %v", r)
	}
}

func TestVerifyAccountWithNoToken(t *testing.T) {
	setupAuth(t)
	b := &backend{}
	r := b.VerifyAccount()
	if r["expired"] != true {
		t.Fatalf("压根没令牌该算 expired（界面据此显示登录页），实得 %v", r)
	}
}

// ---- 改服务器地址：错的不能让它存进去 ----

func TestSetLoginServerValidation(t *testing.T) {
	setupAuth(t)
	b := &backend{}

	// 🩸 非本机的 http 必须被拒 —— 存进去的话以后每次登录都把密码明文发出去 ✗
	for _, bad := range []string{"http://evil.com", "", "ftp://a.com", "https://a.com/x/y"} {
		if err := b.SetLoginServer(bad); err == "" {
			t.Errorf("地址 %q 该被拒 ✗", bad)
		}
	}
	// 合法的：会被规范化（补协议）
	if err := b.SetLoginServer("mc.tinyaii.cn:8444"); err != "" {
		t.Fatalf("合法地址该通过，实得 %q", err)
	}
	if got := loginBase(); got != "https://mc.tinyaii.cn:8444" {
		t.Fatalf("存进去的该是规范化后的地址，实得 %q", got)
	}
	// 本地明文（回环）是允许的 —— 开发和自测要用
	if err := b.SetLoginServer("http://127.0.0.1:8443"); err != "" {
		t.Fatalf("回环的 http 该允许，实得 %q", err)
	}
}
