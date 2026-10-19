package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 版本比较 —— 这条直接决定"更新提示"的对错：比错了要么漏推（用户一直用着
// 旧版却以为是最新），要么误推（天天提示有新版本，用户很快就学会无视它）。
func TestNewerThan(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        bool
		why         string
	}{
		{"1.0.0", "1.0.0", false, "一样"},
		{"1.0.0", "1.0.1", true, "补丁号更大"},
		{"1.0.1", "1.0.0", false, "不许往回提示"},
		{"1.0.0", "1.1.0", true, "次版本更大"},
		{"1.9.0", "1.10.0", true, "🩸 两位数的段：按字符串比会得出 1.10 < 1.9 的错结论"},
		{"1.10.0", "1.9.0", false, "反过来也不能错"},
		{"1.0.0", "2.0.0", true, "大版本"},
		{"1.0", "1.0.0", false, "段数不一样：短的补 0，算相等"},
		{"1.0", "1.0.1", true, "短的补 0 之后再比"},
		{"v1.0.0", "v1.2.0", true, "带 v 前缀也认"},
		{"1.0.0.0", "1.0.0", false, "多出来的 0 段算相等"},
		{"1.0.0", "1.0.0-beta", false, "认不出的形状 ⇒ 不提示"},
		{"", "1.0.0", false, "空的当认不出"},
		{"1.0.0", "", false, "更新源没给版本号 ⇒ 不提示"},
	}
	for _, c := range cases {
		if got := newerThan(c.cur, c.latest); got != c.want {
			t.Errorf("newerThan(%q, %q) = %v，该是 %v（%s）", c.cur, c.latest, got, c.want, c.why)
		}
	}
}

func TestParseVersion(t *testing.T) {
	ok := []string{"1", "1.0", "1.2.3", " v1.2.3 ", "10.20.30", "1.0.0.0"}
	for _, s := range ok {
		if _, good := parseVersion(s); !good {
			t.Errorf("parseVersion(%q) 该认，却说认不出", s)
		}
	}
	bad := []string{"", "   ", "v", "1.a", "1..2", "1.-1", "abc", "1.2.3-beta"}
	for _, s := range bad {
		if _, good := parseVersion(s); good {
			t.Errorf("parseVersion(%q) 该判认不出，却说认得出", s)
		}
	}
}

// ── 更新源签名（M10，2026-10-17）────────────────────────────
//
// 📌 这里用的是**测试专用**密钥对（不是生产的）——
//
//	生产私钥在 dist/update-signing-key.txt，绝不进 git ✗
const (
	testUpdPub  = "M2nuUBfHenLaJ+eCatq1zoqp9iI8wouph1QuaJ2hR0I="
	testUpdPriv = "1UrZsAAZfXOgOmIFedjOukEUPponx0FbeX+7vokrdAUzae5QF8d6cton54Jq2rXOiqn2IjzCi6mHVC5onaFHQg=="
)

// useTestKey 把验签公钥换成测试那对，返回还原函数 ✓
func useTestKey(t *testing.T) {
	t.Helper()
	old := updatePubKey
	updatePubKey = testUpdPub
	t.Cleanup(func() { updatePubKey = old })
}

// signBody 造一份**签好名**的 latest.json ✓（规则要跟 signedFields 一致）
// sigOf 算出签名（规则必须跟 signedFields 一致）✓
func sigOf(version, url, note string) string {
	privB64, _ := base64.StdEncoding.DecodeString(testUpdPriv)
	sig := ed25519.Sign(ed25519.PrivateKey(privB64), []byte(version+"\n"+url+"\n"+note))
	return base64.StdEncoding.EncodeToString(sig)
}

// signBody 造一份**签好名**的 latest.json ✓
func signBody(version, url, note string) string {
	return fmt.Sprintf(`{"version":%q,"url":%q,"note":%q,"sig":%q}`,
		version, url, note, sigOf(version, url, note))
}

// fetchFrom 遇到"对面不正常"时的几种情况。
//
// 这条特别值得写：更新源是**外面的东西**，它返回什么我们控制不了 ——
// 而这段代码跑在用户的**启动路径**上 ✗ 任何一条没兜住都可能让启动卡住或崩掉。
//
// 🩸 M10 之后：**没有签名或签名不对，一律当"这次没查成"** ✓
func TestFetchFrom(t *testing.T) {
	useTestKey(t)
	good := signBody("1.2.3", "https://ti.tinyaii.cn/", "修了点什么")

	cases := []struct {
		name string
		code int
		body string
		want string // 期望拿到的版本号；空 = 该报错
	}{
		{"正常（签名对）", 200, good, "1.2.3"},
		{"只给了版本号+空地址（签过）", 200, signBody("2.0.0", "https://ti.tinyaii.cn/", ""), "2.0.0"},
		{"服务器 500", 500, `{}`, ""},
		{"服务器 404", 404, `not found`, ""},
		{"返回的不是 JSON", 200, `hello world`, ""},
		{"JSON 里没有版本号", 200, `{"url":"https://ti.tinyaii.cn/"}`, ""},
		{"版本号只有空白", 200, `{"version":"   "}`, ""},
		// ⭐ M10 起这几条必须全都失败 ✓
		{"没有签名", 200, `{"version":"9.9.9","url":"https://ti.tinyaii.cn/","note":""}`, ""},
		{"签名是乱写的", 200, `{"version":"9.9.9","url":"https://ti.tinyaii.cn/","note":"","sig":"AAA"}`, ""},
		// 这份的签名是用 1.2.3 签的，但 version 被改成了 9.9.9 ⇒ 签名必须对不上 ✓
		{"签名对不上内容（version 被改）", 200,
			fmt.Sprintf(`{"version":"9.9.9","url":"https://ti.tinyaii.cn/","note":"x","sig":%q}`,
				sigOf("1.2.3", "https://ti.tinyaii.cn/", "x")), ""},
		{"下载页域名不在白名单（签过也不收）", 200, signBody("9.9.9", "https://evil.example.com/x", "x"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.code)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()

			info, err := fetchFrom(srv.URL)
			if c.want == "" {
				if err == nil {
					t.Fatalf("该报错，却拿到了 %+v", info)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if info.Version != c.want {
				t.Errorf("版本号该是 %q，实得 %q", c.want, info.Version)
			}
		})
	}
}

// ⭐ 下载页白名单：https + 域名（含子域）才算合格
//
// 反证方法：把 updateAllowedHosts 清空 ⇒ 立刻红 ✓
func TestHostAllowed(t *testing.T) {
	ok := []string{
		"https://ti.tinyaii.cn/",
		"https://tinyaii.cn/dl",
		"https://github.com/TinyAII/ti-link/releases",
		"https://tinyaii.github.io/ti-link/",
	}
	bad := []string{
		"http://ti.tinyaii.cn/",        // 不是 https
		"https://evil.example.com/",    // 不在白名单
		"https://tinyaii.cn.evil.com/", // 后缀骗术（tinyaii.cn 不是它的域名）
		"https://evilgithub.com/",      // 同理
		"", "not a url", "javascript:alert(1)",
	}
	for _, u := range ok {
		if !hostAllowed(u) {
			t.Errorf("该放行：%s", u)
		}
	}
	for _, u := range bad {
		if hostAllowed(u) {
			t.Errorf("该拒绝：%s ✗（白名单被绕过了）", u)
		}
	}
}

// 对面给一大坨东西时不能被拖死 —— 读的时候有 64K 上限。
func TestFetchFromCapsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("A", 300*1024)))
	}))
	defer srv.Close()

	// 300KB 被截到 64K ⇒ 不是合法 JSON ⇒ 报错。
	// 断言的是"它没有乖乖把 300KB 全读进来"，也就是那道上限真起了作用。
	if _, err := fetchFrom(srv.URL); err == nil {
		t.Error("一大坨非 JSON 该报错，说明 64K 上限没起作用")
	}
}

// ⚠️ 每个更新源都必须是 https —— 明文上的 JSON 能被中间人改掉，
// 就能把用户引到假下载页去。加源的时候别忘这条。
func TestLatestURLsAreHTTPS(t *testing.T) {
	if len(latestURLs) == 0 {
		t.Fatal("一个更新源都没配 —— 更新检查会永远失败")
	}
	for _, u := range latestURLs {
		if !strings.HasPrefix(u, "https://") {
			t.Errorf("更新源 %q 不是 https", u)
		}
	}
}
