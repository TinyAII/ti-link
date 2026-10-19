package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"p2plink/internal/auth"
)

// ⭐⭐ 一码一人 —— 这次是**从外面打进来**再验一遍。
//
// auth 包里那条并发测试验的是那一层的锁；这条验的是"走 HTTP 这条路"
// 也是同样的结论 ✓（比如哪天有人在 HTTP 层加了个"先查再调"的缓存，它就能抓住）
func TestSignupOneCodeOnlyOnceOverHTTP(t *testing.T) {
	ts, srv, st := newTest(t)
	codes, err := st.GenCodes(1, "race", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	const n = 4
	// 🩸 **每个请求用不同的邮箱**，两个理由，第二个是要紧的那个：
	//  ① 同一个邮箱要 4 个验证码会被"60 秒重发间隔"挡掉；
	//  ② 如果 4 个请求**共用同一个验证码**，那个码本身只用一次 ⇒
	//     测试会**因为验证码那条路**通过，而不是因为邀请码那条路 ⇒
	//     邀请码的提交段复核坏了，它照样是绿的 ✗ 这就是"假绿"。
	// 所以：一个邀请码、四个不同的人来抢 —— 这才是要验的那件事 ✓
	type req struct{ email, mailCode string }
	reqs := make([]req, n)
	for i := range reqs {
		email := fmt.Sprintf("race%d@qq.com", i)
		mc, why := srv.mailCodes.beginSend(auth.NormalizeEmail(email), "127.0.0.1", srv.now())
		if why != "" {
			t.Fatalf("第 %d 个验证码没拿到: %s", i, why)
		}
		reqs[i] = req{email, mc}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount := 0
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			b, _ := json.Marshal(map[string]any{
				"invite": codes[0], "email": reqs[i].email,
				"password": "goodpassword", "mailCode": reqs[i].mailCode,
			})
			req, _ := http.NewRequest("POST", ts.URL+"/api/signup", strings.NewReader(string(b)))
			req.Header.Set("Content-Type", "application/json")
			resp, err := ts.Client().Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				mu.Lock()
				okCount++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if okCount != 1 {
		t.Fatalf("一张邀请码被 4 个人并发抢，该只有 1 个成功，实得 %d ✗", okCount)
	}
}

// 密码**不能**原样出现在响应里，也不能出现在账号文件里。
func TestNoPlaintextPasswordStored(t *testing.T) {
	ts, srv, st := newTest(t)
	c := newClient(t, ts)
	codes, _ := st.GenCodes(1, "", "", time.Now())
	mc, why := srv.mailCodes.beginSend(auth.NormalizeEmail("a@qq.com"), "127.0.0.1", srv.now())
	if why != "" {
		t.Fatal(why)
	}

	const pass = "MyPlaintextPass123"
	code, out, _ := do(t, c, "POST", ts.URL+"/api/signup",
		map[string]any{"invite": codes[0], "email": "a@qq.com",
			"password": pass, "mailCode": mc}, nil)
	if code != http.StatusOK {
		t.Fatalf("注册失败：%d %v", code, out)
	}
	if _, ok := out["password"]; ok {
		t.Error("响应里回了密码字段 ✗")
	}
	raw, err := os.ReadFile(filepath.Join(st.Dir(), auth.AccountsFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), pass) {
		t.Fatal("🩸 账号文件里出现了明文密码 —— 存的该是 bcrypt 哈希 ✗")
	}
}

// 后台页面本身不鉴权（它是个空壳、一条数据都没有），但**接口一律不放开**。
// 这条把两件事的边界钉住：/admin/ 可以裸奔，/api/admin/ 不行 ✓
func TestAdminPageIsPublicButApiIsNot(t *testing.T) {
	ts, _, _ := newTest(t)
	if code, _, _ := do(t, ts.Client(), "GET", ts.URL+"/admin/", nil, nil); code != http.StatusOK {
		t.Fatalf("后台页面该能打开（没数据），实得 %d", code)
	}
	for _, p := range []string{"/api/admin/accounts", "/api/admin/codes", "/api/admin/session"} {
		if code, _, _ := do(t, ts.Client(), "GET", ts.URL+p, nil, nil); code != http.StatusUnauthorized {
			t.Errorf("%s 该 401，实得 %d ✗", p, code)
		}
	}
}

// 静态资源要真的取得到（路径写错的话页面会白屏，而且很难一眼看出来）。
func TestStaticAssetsServed(t *testing.T) {
	ts, _, _ := newTest(t)
	for _, p := range []string{"/admin/", "/admin/app.js", "/admin/style.css"} {
		code, _, _ := do(t, ts.Client(), "GET", ts.URL+p, nil, nil)
		if code != http.StatusOK {
			t.Errorf("%s 该 200，实得 %d ✗", p, code)
		}
	}
	// 裸的 /admin 该跳到 /admin/
	c := ts.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Get(ts.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Errorf("/admin 该 301 到 /admin/，实得 %d", resp.StatusCode)
	}
}

// 没带 token 的 /api/me 要 401 —— 顺带确认 Bearer 解析不会被奇怪的头糊弄。
func TestBearerParsing(t *testing.T) {
	ts, srv, st := newTest(t)
	tok := signUp(t, ts, srv, st, "a@qq.com")
	c := newClient(t, ts)

	good := []string{"Bearer " + tok, "bearer " + tok, "BEARER " + tok}
	for _, v := range good {
		if code, _, _ := do(t, c, "GET", ts.URL+"/api/me", nil,
			map[string]string{"Authorization": v}); code != http.StatusOK {
			t.Errorf("Authorization: %q 该认，实得 %d", v, code)
		}
	}
	bad := []string{"", "Bearer", "Bearer ", "Basic " + tok, tok, "Bearer " + tok + "extra"}
	for _, v := range bad {
		if code, _, _ := do(t, c, "GET", ts.URL+"/api/me", nil,
			map[string]string{"Authorization": v}); code == http.StatusOK {
			t.Errorf("Authorization: %q 不该认 ✗", v)
		}
	}
}

// 本地 `-dev-tls` 那张自签证书要真的能用 —— 这条搬到 internal/tlsconf 那边了
// （它是那个包的职责，放在面板里会让 `go test ./internal/tlsconf/` 显示"没有测试"，
//  看着像没测过一样 ✗）
