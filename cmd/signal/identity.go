package main

import (
	"runtime"
	"runtime/debug"
)

// 服务身份：挂屏/排查时**一眼看出"这是哪个版本在跑"** ✓
//
// 📌 这些值不靠 `-ldflags` 注入，而是从**编译信息**里读（`debug.ReadBuildInfo`）——
// 少一个要维护的注入点：省得构建脚本忘了传、版本号静默变空 ✗
// 代价是必须在 **git 仓库里**构建才会带上 commit（部署脚本就是在仓库里编的 ✓）；
// 不在仓库里编的话，这里就是空的 —— 界面显示"—"，不编假数 ✓
var (
	appRevision string // git 短哈希；有未提交改动会带个后缀
	appBuiltAt  string // **那次提交**的时间（不是构建时间，别搞混）
)

func init() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			appRevision = s.Value
			if len(appRevision) > 12 {
				appRevision = appRevision[:12]
			}
		case "vcs.time":
			appBuiltAt = s.Value
		case "vcs.modified":
			// ⚠️ 编出来的东西跟提交不一致时必须说清 ——
			// 不然排查时会拿着一个"看着像某个提交"的二进制对代码 ✗
			if s.Value == "true" {
				appRevision += "+未提交"
			}
		}
	}
}

// identityNow 是"这是哪个版本在跑"。跟指标、配置都不同类，单独一组 ✓
// 📌 跟 limits 分开：那边是"允许多少"，这边是"是谁" ✓
func identityNow() map[string]any {
	return map[string]any{
		"revision":   appRevision,
		"built_at":   appBuiltAt,
		"go_version": runtime.Version(),
		"started_at": startedAt.Unix(),
	}
}
