// 前端调的每个 window.Xxx 都必须在 Go 这边 Bind 过。
//
// 🩸 为什么值得单独一条测试：这类失手**不会报错**。
// 拼错一个名字（比如 Go 里叫 Logout、页面里写成 LogOut）之后，
// 表现为"点了按钮没反应" —— 控制台里只有一行 TypeError，
// 而用户看到的是一个安静的、什么都不做的按钮，最难查 ✗
//
// 做法：把两边**各自扫出来对一遍**。名字是同一件事的两半，
// 只有靠机械比对才不会漏（跟"Go 的词库和前端 GAMES 不能分叉"是同一套路子）✓
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// boundNames 扫 main.go 里所有 w.Bind("Xxx", ...)
func boundNames(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`w\.Bind\("([A-Za-z0-9_]+)"`)
	out := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		out[m[1]] = true
	}
	if len(out) < 10 {
		t.Fatalf("只扫到 %d 个 Bind —— 扫描逻辑是不是失效了？", len(out))
	}
	return out
}

// frontendNames 扫 ui/js/*.js 里所有 window.Xxx 的用法。
//
// 只认**首字母大写**的 —— 那是我们自己的命名习惯；
// 浏览器自带的都是小写（window.setTimeout / location / addEventListener …），
// 还有 window.__ready 这种带下划线的，都不该被算进来 ✓
func frontendNames(t *testing.T) map[string][]string {
	t.Helper()
	dir := filepath.Join("ui", "js")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`window\.([A-Z][A-Za-z0-9_]*)`)
	out := map[string][]string{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			// 同一个名字在同一文件里出现多次只记一次
			dup := false
			for _, f := range out[m[1]] {
				if f == e.Name() {
					dup = true
					break
				}
			}
			if !dup {
				out[m[1]] = append(out[m[1]], e.Name())
			}
		}
	}
	if len(out) < 5 {
		t.Fatalf("只扫到 %d 个 window.Xxx —— 扫描逻辑是不是失效了？", len(out))
	}
	return out
}

func TestFrontendBindsAllExist(t *testing.T) {
	bound := boundNames(t)
	used := frontendNames(t)

	var missing []string
	for name, files := range used {
		if !bound[name] {
			sort.Strings(files)
			missing = append(missing, name+"（用在 "+strings.Join(files, "、")+"）")
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("前端调了这些 window.Xxx，但 Go 那边没 Bind：\n  %s\n"+
			"（症状是「点了按钮没反应」，控制台里只有一行 TypeError）",
			strings.Join(missing, "\n  "))
	}
}

// 反过来也看一半：Go 绑了但前端一次都没用的，不报错，只提示 ——
// 有可能是真没人用了（该删），也有可能只是名字对不上（上面那条会先红）✓
func TestBindsNotUsedByFrontend(t *testing.T) {
	bound := boundNames(t)
	used := frontendNames(t)
	ignored := map[string]bool{
		// __ready 是给窗口用的（08-window.js 里调的是 window.__ready，不走这条扫描）✓
	}

	var unused []string
	for name := range bound {
		if len(used[name]) == 0 && !ignored[name] {
			unused = append(unused, name)
		}
	}
	if len(unused) > 0 {
		sort.Strings(unused)
		t.Logf("⚠️ 这些绑定前端一次都没用到（可能已经废弃了）：%s", strings.Join(unused, "、"))
	}
}
