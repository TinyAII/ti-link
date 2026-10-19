// 更新检查：拉一份静态 JSON，跟自己的版本号比一比。
//
// 📌 为什么更新源是**静态托管**（GitHub raw）而不是自己的服务器：
//
//	① 更新检查要是走信令服务器，等于又给自己加了一层"在运营服务"的痕迹；
//	② 信令服务一挂、更新检查也跟着挂 —— 两件不相干的事没必要绑在一起；
//	③ 静态文件不要钱、不要备案、不用维护。
//
// ⚠️ 只检查、**不自动下载替换**。无签名的 exe 自我替换在 Windows 上
// 很容易被杀软当成木马，那比"用户多按两次鼠标"糟糕得多。
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// latestURLs 是更新源，**按顺序依次尝试**，第一个能解析的就算数。
//
// 📌 为什么要放好几个：这几个域名在国内的可达性都不算铁打的 ——
// 实测过，GitHub 的 raw 和 jsDelivr 在这台机器上直接连不上（超时），
// 只有 Pages 通。跟 STUN 服务器那套一个思路（见 netutil.DefaultStunServers）：
// 多给几个，一个不通还有下一个，总比"更新检查永远是失败的"强 ✓
//
// 📌 自己域名排第一：`*.github.io` 在国内**经常被 DNS 污染**（连解析这一步都会挂），
// 而 `ti.tinyaii.cn` 走阿里云解析、拿到的一定是 GitHub 的正确 IP。IP 还是人家的、
// 速度不会变快，但至少"能不能解析出来"这一层干净了 ✓
//
// ⚠️ 每个都必须是 https —— 明文 http 上的 JSON 能被中间人改掉，
// 就能把用户引到假下载页去。测试里有一条专门盯着这个 ✓
var latestURLs = []string{
	"https://ti.tinyaii.cn/latest.json",                                  // 自己的域名（主）
	"https://tinyaii.github.io/ti-link/latest.json",                      // GitHub Pages 默认域名（备）
	"https://cdn.jsdelivr.net/gh/TinyAII/ti-link@main/latest.json",       // jsDelivr CDN（备）
	"https://raw.githubusercontent.com/TinyAII/ti-link/main/latest.json", // raw（最后的备）
}

// officialSite 是官网首页，也是下载页的兜底。
//
// 🩸 为什么要兜底：下载地址是 latest.json 里的 url 字段，**服务端说了算**。
// 万一哪天那份 JSON 漏了 url（或者格式改了、字段拼错），
// 页面那边就会把「去下载」按钮藏起来 —— 用户看到一个"有新版本但没处点"的弹窗，
// 完全不知道该去哪 ✗ ⇒ url 空着就直接送官网，至少还有路可走 ✓
const officialSite = "https://ti.tinyaii.cn/"

// updateInfo 就是更新源那份 JSON 的形状。
type updateInfo struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Note    string `json:"note"`

	// Sig 是 version/url/note 三个字段的 Ed25519 签名（base64）。
	// 🩸 **必填**：没有它或验不过，这次更新一律当"没查成" ✓
	Sig string `json:"sig"`
}

// updatePubKey 是更新源签名的公钥（Ed25519，base64）。
//
// 🩸 **它是唯一能证明"这份 latest.json 是我们发的"的东西** ——
// 私钥在老板手上（`dist/update-signing-key.txt`，不进 git），
// 能改那个 JSON 的人没有私钥就签不出来 ✓
//
// ⚠️ 换密钥 = 必须**重发客户端**（旧客户端里内嵌的是旧公钥 ✗）
//
// 📌 为什么是 var 不是 const：测试要把它临时换成**测试专用密钥对**，
// 这样才能验"好签名能过、坏签名过不去" —— 生产的私钥当然不能进 git ✗
// （同包内才改得到，包外改不了 ✓）
var updatePubKey = "bOeOVn1KLvdbZHm2RrN2JiA1sU5v/fkKEF24L48uc0M="

// signedFields 是"哪些字段被签了名"，按固定顺序拼。
//
// 🩸 为什么拼字符串、不签原始 JSON：JSON 的空白/字段顺序不固定，
// 同一份内容能编出好几种字节串 ⇒ 签名必对不上 ✗
// 固定成 "version\nurl\nnote" 就稳了 ✓ 发版签名脚本按**同样规则**拼 ✓
func signedFields(info updateInfo) []byte {
	return []byte(info.Version + "\n" + info.URL + "\n" + info.Note)
}

// verifyUpdate 验签。没过就返回错误 ✓
//
// 反证方法：把 updatePubKey 改一个字符 ⇒ 测试里"好签名"那条立刻红 ✓
func verifyUpdate(info updateInfo) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(info.Sig))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("更新源没带有效签名")
	}
	pub, err := base64.StdEncoding.DecodeString(updatePubKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("客户端内置的公钥有问题")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), signedFields(info), sig) {
		return errors.New("更新源的签名对不上")
	}
	return nil
}

// updateAllowedHosts 是"下载页只许在这些域名下"的清单（跟签名双保险）✓
//
// 📌 签名已经保护了 url 字段 —— 这个是**第二道**：
// 万一哪天私钥泄了，攻击者也只能把链接指到这些域名下，指不到别处 ✓
var updateAllowedHosts = []string{"tinyaii.cn", "github.com", "tinyaii.github.io"}

// hostAllowed 判断下载页地址合不合格：必须 https，且域名在白名单里（含子域）✓
func hostAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, allow := range updateAllowedHosts {
		if h == allow || strings.HasSuffix(h, "."+allow) {
			return true
		}
	}
	return false
}

// fetchLatest 依次试每个更新源，第一个成的就返回。
//
// 超时给得很短（每个 4 秒）：它挂在**启动路径**上，网络慢的时候绝不能拖住窗口。
// 全都不通就返回错误，调用方一律当"这次没查成" —— 不能因为更新服务挂了，
// 就让用户以为自己的软件出了问题 ✗
func fetchLatest() (*updateInfo, error) {
	var lastErr error
	for _, u := range latestURLs {
		if !strings.HasPrefix(u, "https://") {
			lastErr = fmt.Errorf("更新源 %s 不是 https", u)
			continue
		}
		info, err := fetchFrom(u)
		if err == nil {
			return info, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("没有配置更新源")
	}
	return nil, lastErr
}

// fetchFrom 从**一个**地址拉更新信息。
//
// 拆出来是为了能测：测试里用 httptest 起个本地服务器，就能把
// "服务器 500 / 返回的不是 JSON / 没给版本号 / 给了一大坨东西"这些都过一遍，
// 而不用真好几个域名都活着 ✓
func fetchFrom(url string) (*updateInfo, error) {
	c := &http.Client{Timeout: 4 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// 有些 CDN 见到没有 UA 的请求会直接拒掉
	req.Header.Set("User-Agent", "TILink/"+appVersion)

	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("更新源返回 %d", resp.StatusCode)
	}

	// 限制读进来的字节数：万一对面给了个几百 MB 的东西（或者被劫持），
	// 不能让它把内存吃光。更新源本来就只有几百字节，64K 绰绰有余。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}

	var info updateInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, err
	}
	if strings.TrimSpace(info.Version) == "" {
		return nil, errors.New("更新源里没有版本号")
	}
	// 🩸 验签 + 白名单都在**这一层**做：某个源被污染了 ⇒ 这个名字直接失败，
	// 换下一个源继续试 ✓（全都不通就报"这次没查成"，绝不拿没验过的数据去提示用户 ✗）
	if err := verifyUpdate(info); err != nil {
		return nil, err
	}
	if !hostAllowed(info.URL) {
		return nil, fmt.Errorf("下载页域名不在白名单：%s", info.URL)
	}
	return &info, nil
}

// newerThan 判断 latest 是不是比 cur 新。两个都是 "1.2.3" 这种形状。
//
// 🩸 为什么不能直接比字符串：**"1.10.0" < "1.9.0"** —— 按字符串比，
// 10 会比 9 小，于是新版推不出去 ✗ 所以要按点切开、逐段比数字。
// 段数不一样时短的补 0（"1.1" 和 "1.1.0" 算相等）✓
//
// 认不出来的形状一律返回 false —— **宁可不提示，也不要拿一个错的结论去打扰用户** ✓
func newerThan(cur, latest string) bool {
	c, ok1 := parseVersion(cur)
	l, ok2 := parseVersion(latest)
	if !ok1 || !ok2 {
		return false
	}
	n := len(c)
	if len(l) > n {
		n = len(l)
	}
	for i := 0; i < n; i++ {
		var a, b int
		if i < len(c) {
			a = c[i]
		}
		if i < len(l) {
			b = l[i]
		}
		if a != b {
			return b > a
		}
	}
	return false
}

// parseVersion 把 "v1.2.3" 拆成 [1 2 3]。任何一段不是非负整数就整条判认不出。
func parseVersion(s string) ([]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

/* ---------- 最近一次查到的更新 ---------- */

// 页面和 Go 侧要交流"现在有没有待更新"。存整条（不只 URL）——
// 弹窗上要写"新版本 v1.1.0 可以下载了"，版本号得从这儿来 ✓
var (
	updateMu     sync.Mutex
	pending      *updateInfo // 待更新（nil = 没有）
	pendingShown bool        // 启动那条路是不是已经弹过了
)

func setPendingUpdate(info *updateInfo) {
	updateMu.Lock()
	pending = info
	pendingShown = false
	updateMu.Unlock()
}

// pendingUpdate 给**手动点「检测更新」**那条路用：他主动问的，每次都给 ✓
func pendingUpdate() *updateInfo {
	updateMu.Lock()
	defer updateMu.Unlock()
	return pending
}

// takeStartupUpdate 给**启动时静默检查**那条路用：取走一次。
//
// 叫 take 是因为它只交付一次 —— 一次启动最多弹一个窗，
// 不会因为页面刷新、或者来回进设置页就反复弹出来烦人 ✗
func takeStartupUpdate() *updateInfo {
	updateMu.Lock()
	defer updateMu.Unlock()
	if pending == nil || pendingShown {
		return nil
	}
	pendingShown = true
	return pending
}
