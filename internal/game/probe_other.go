//go:build !windows

package game

import "fmt"

// 非 Windows 平台的兜底：这些探针做不了。
//
// 我们的 GUI 本身只有 Windows 版，正常跑不到这儿 ——
// 留着它只是为了让 `go build ./...` / `go vet ./...` 在别的系统上也能过 ✓
//
// 🩸 返回错误 = "查不动" ⇒ 上层会当成**说不准** ⇒ **放行** ✓
// 不会瞎报"游戏没开" —— 这是有意的 ✓ 宁可不给判断，也别给错的 ✓
func processPathsOnPort(port int) (paths []string, readNames int, err error) {
	return nil, 0, fmt.Errorf("这个平台不支持查端口占用")
}
