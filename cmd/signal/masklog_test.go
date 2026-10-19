// 服务端日志里不许出现**没打码的地址** —— 这条用扫源码的方式钉住。
//
// 为什么值得这么钉：打码其实只在 main.go 的 handle() 入口做了一次
// （maskedRemote := netutil.MaskAddr(raw)），后面所有日志拿到的都是打码后的值 ✓
// 但**以后有人新加一条日志**、顺手写了 member.remoteIP（或者别的没打码的地址），
// 人肉评审很容易漏过去 ✗ —— 而这类漏失的代价是"用户的公网 IP 进了日志"，
// 公开分发之后，那属于收集个人信息。
//
// 所以干脆把同目录下所有 log 调用扫一遍：参数里不许出现名字像地址、又没套
// MaskAddr 的变量。**命名约定是这条防线的一部分**：
//
//	maskedRemote / xxxMasked / xxxRedacted → 当作已打码，放行 ✓
//	remote / raw / ip / addr / remoteIP    → 违例 ✗
//
// 也就是说：谁要是把 maskedRemote 改回 remote，这条测试会红 —— 那是故意的。
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerLogsCarryMaskedAddrs(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", f, err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isLogCall(call) {
				return true
			}
			scanned++
			for _, arg := range call.Args {
				if name, bad := rawAddrArg(arg); bad {
					t.Errorf("%s: 日志参数里是没打码的 %s —— 套一下 netutil.MaskAddr，"+
						"或者把变量名带上 masked", fset.Position(arg.Pos()), name)
				}
			}
			return true
		})

		// 光看名字还不够：还得确认那个 masked* 是**真的**过了 MaskAddr。
		// 不然把 `maskedRemote := netutil.MaskAddr(raw)` 改成 `:= raw`，
		// 上面那圈按名字的扫描照样"通过" ✗ —— 名字把它骗过去了。
		ast.Inspect(af, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range as.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || !strings.Contains(strings.ToLower(id.Name), "masked") {
					continue
				}
				if i >= len(as.Rhs) || !isMaskCall(as.Rhs[i]) {
					t.Errorf("%s: %s 的名字里写了 masked，赋的却不是 MaskAddr(...) —— 名字骗人 ✗",
						fset.Position(as.Pos()), id.Name)
				}
			}
			return true
		})
	}

	// 🩸 这条是防"测试空转"的：文件列表变了、或者解析根本没进到 log 调用里，
	// 上面那个循环会一条都不报、看着像"通过" ✗
	if scanned < 5 {
		t.Errorf("只扫到 %d 条 log 调用，太少了 —— 扫描逻辑是不是失效了？", scanned)
	}
	t.Logf("扫了 %d 条 log 调用", scanned)
}

func isLogCall(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "log" {
		return false
	}
	switch sel.Sel.Name {
	case "Print", "Printf", "Println", "Fatal", "Fatalf", "Fatalln", "Panicf":
		return true
	}
	return false
}

// rawAddrArg 判断这个参数是不是"一个没打码的地址变量"。
// 套了 netutil.MaskAddr(...) 的放行；别的调用（字符串拼接、dispName 之类）不管。
func rawAddrArg(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.CallExpr:
		if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "MaskAddr" {
			return "", false
		}
		return "", false
	case *ast.Ident:
		if looksLikeRawAddr(v.Name) {
			return v.Name, true
		}
	case *ast.SelectorExpr:
		if looksLikeRawAddr(v.Sel.Name) {
			if id, ok := v.X.(*ast.Ident); ok {
				return id.Name + "." + v.Sel.Name, true
			}
			return v.Sel.Name, true
		}
	}
	return "", false
}

// isMaskCall 判断这个表达式是不是 netutil.MaskAddr(...) 的调用。
func isMaskCall(e ast.Expr) bool {
	c, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := c.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "MaskAddr"
}

// looksLikeRawAddr：名字像地址、又没自己声明"已打码"的，一律怀疑。
func looksLikeRawAddr(name string) bool {
	l := strings.ToLower(name)
	if strings.Contains(l, "masked") || strings.Contains(l, "redact") {
		return false // 名字自己声明过了 ✓
	}
	switch l {
	case "remote", "raw", "ip", "addr", "address", "remoteip", "clientip", "peer", "peerip":
		return true
	}
	return false
}
