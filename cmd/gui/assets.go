// 界面资源：把拆开的 index.html / style.css / app.js 拼成一整页，并内联图片。
//
// 为什么要拼：webview 用的是 SetHtml()，它**加载不了外部文件**，
// 所以 CSS/JS/图标只能由 Go 拼好、以 data URL 塞进去 —— 拆开只是为了好维护，
// 编出来仍然是单文件 exe。
package main

import (
	"embed"
	"encoding/base64"
	"html"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"p2plink/internal/identity"
	"p2plink/internal/nick"
)

//go:embed ui/index.html
var uiIndex string

//go:embed ui/style.css
var uiStyle string

//go:embed ui/js
var uiJSFS embed.FS

// uiApp 把 ui/js/*.js 按**文件名顺序**拼成一份，当一份脚本用。
//
// 📌 为什么是"拼"而不是 ES module：webview 走的是 file://，
// ES module 会被 CORS 挡掉 ✗ ⇒ 只能拼成一份普通的 <script>。
// 于是**文件名的数字前缀就是执行顺序**（前端没有模块系统，全靠拼的顺序）——
// 别改成随便的排序，也别插一个不带头数字的文件 ✗
//
// 🩸 拆分时保证过：拼出来的内容和原来那份 app.js **逐字节一致**，
// 所以这是纯搬家、行为零变化 ✓ 以后往任何一块里加东西都行，但**别调顺序**。
func uiApp() string {
	ents, err := fs.ReadDir(uiJSFS, "ui/js")
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".js") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var b strings.Builder
	for _, n := range names {
		src, err := fs.ReadFile(uiJSFS, "ui/js/"+n)
		if err != nil {
			continue
		}
		b.Write(src)
	}
	return b.String()
}

//go:embed icon-mc.png
var iconMC []byte

//go:embed icon-terraria.png
var iconTerraria []byte

//go:embed icon-stardew.png
var iconStardew []byte

//go:embed room-cover.jpg
var roomCover []byte

//go:embed cover-terraria.jpg
var coverTerraria []byte

//go:embed cover-stardew.jpg
var coverStardew []byte

//go:embed cover-survival.jpg
var coverSurvival []byte

//go:embed cover-dst.jpg
var coverDST []byte

//go:embed cover-palworld.jpg
var coverPalworld []byte

//go:embed cover-rusted.jpg
var coverRusted []byte

//go:embed cover-ark.jpg
var coverArk []byte

//go:embed cover-asa.jpg
var coverASA []byte

//go:embed cover-mindustry.jpg
var coverMindustry []byte

//go:embed cover-factorio.jpg
var coverFactorio []byte

//go:embed cover-7dtd.jpg
var cover7DTD []byte

//go:embed cover-vrising.jpg
var coverVRising []byte

//go:embed tutorial/*.jpg
var tutorialFS embed.FS

//go:embed avatar/*.png
var avatarFS embed.FS

// avatarFiles 是内置头像库，**顺序就是索引** ——
// 协议里传的是这个下标（一个数字），两端各自拿它来查自己内置的这张图。
//
// ⚠️ 只能往后追加，不能插队、不能改动已有顺序：
// 索引是跨端约定，一插队老客户端就会给别人显示错头像。
// ⚠️ 也别删中间某张 —— 后面的全部会前移一位。真要删就在末尾删，
// 或者把那张换成新图（占位也不能空）。
var avatarFiles = []struct{ File, Group string }{
	{"av00.png", "我的世界"},
	{"av01.png", "我的世界"},
	{"av02.png", "我的世界"},
	{"av03.png", "我的世界"},
	{"av04.png", "我的世界"},
	{"av05.png", "我的世界"},
	{"av06.png", "泰拉瑞亚"},
	{"av07.png", "星露谷物语"},
	{"av08.png", "星露谷物语"},
	{"av09.png", "星露谷物语"},
	{"av10.png", "星露谷物语"},
	{"av11.png", "星露谷物语"},
	{"av12.png", "星露谷物语"},
	{"av13.png", "星露谷物语"},
	{"av14.png", "星露谷物语"},
	{"av15.png", "星露谷物语"},
	{"av16.png", "星露谷物语"},
}

// AvatarCount 是内置头像张数。设置里存头像索引时用它卡范围。
func AvatarCount() int { return len(avatarFiles) }

// avatarListJS 把头像库拼成一个 JS 数组字面量：[{src:"data:...", g:"分类"}, ...]
//
// 跟图标一样走 data URL 内联 —— webview 的 SetHtml() 加载不了外部文件。
func avatarListJS() string {
	var b strings.Builder
	b.WriteString("[")
	for i, a := range avatarFiles {
		if i > 0 {
			b.WriteString(",")
		}
		data, err := avatarFS.ReadFile("avatar/" + a.File)
		if err != nil {
			b.WriteString("null") // 缺图就留空位，索引不能因此错位
			continue
		}
		b.WriteString(`{g:` + strconv.Quote(a.Group) +
			`,src:` + strconv.Quote(dataURL("image/png", data)) + `}`)
	}
	b.WriteString("]")
	return b.String()
}

// tutorialNames 是教程图的编号，房主篇 01…09（第 4 步两张：04a/04b）+ 加入篇 j1…j7。
var tutorialNames = []string{
	"01", "02", "03", "04a", "04b", "05", "06", "07", "08", "09",
	"j1", "j2", "j3", "j4", "j5", "j6", "j7",
}

func dataURL(mime string, b []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
}

// buildHTML 把样式、脚本、主题、图标、教程图全部填进页面。
func buildHTML() string {
	h := uiIndex
	h = strings.Replace(h, "__CSS__", uiStyle, 1)

	// 头像库和昵称词库要塞进 JS 里，所以得**先把 JS 里的占位符换掉**再整体拼进页面。
	// 词库只有 internal/nick 那一份 —— 客户端摇名字用它，穿透服务器校验也用它 ✓
	js := strings.Replace(uiApp(), "__AVATARS__", avatarListJS(), 1)
	js = strings.Replace(js, "__NICK_WORDS__", nick.WordsJSON(), 1)
	h = strings.Replace(h, "__JS__", js, 1)

	// ⚠️ 这几个用 -1（**全部替换**），不是 1。
	//
	// 一张图可能在页面里出现好几处：封面就同时被选游戏页的 .tile 和
	// 房间页的 .roomcover 引用 ⇒ 只换第一个的话，第二个还留着占位符
	// ⇒ **页面白屏** ✗
	// （assets_test.go 就是专门抓这个的 —— 2026-10-15 给房间页加封面时真抓到了一次）
	h = strings.Replace(h, "__ICON_MC__", dataURL("image/png", iconMC), -1)
	h = strings.Replace(h, "__ICON_TERRARIA__", dataURL("image/png", iconTerraria), -1)
	h = strings.Replace(h, "__ICON_STARDEW__", dataURL("image/png", iconStardew), -1)
	h = strings.Replace(h, "__ROOMCOVER__", dataURL("image/jpeg", roomCover), -1)
	// 游戏卡背景图。暂时和房间封面共用同一张；将来每款游戏各配一张时，
	// 这里改成按游戏各替换一个占位符即可。
	h = strings.Replace(h, "__GAMECOVER__", dataURL("image/jpeg", roomCover), -1)
	h = strings.Replace(h, "__COVER_TERRARIA__", dataURL("image/jpeg", coverTerraria), -1)
	h = strings.Replace(h, "__COVER_STARDEW__", dataURL("image/jpeg", coverStardew), -1)
	h = strings.Replace(h, "__COVER_SURVIVAL__", dataURL("image/jpeg", coverSurvival), -1)
	h = strings.Replace(h, "__COVER_DST__", dataURL("image/jpeg", coverDST), -1)
	h = strings.Replace(h, "__COVER_PALWORLD__", dataURL("image/jpeg", coverPalworld), -1)
	h = strings.Replace(h, "__COVER_RUSTED__", dataURL("image/jpeg", coverRusted), -1)
	h = strings.Replace(h, "__COVER_ARK__", dataURL("image/jpeg", coverArk), -1)
	h = strings.Replace(h, "__COVER_ASA__", dataURL("image/jpeg", coverASA), -1)
	h = strings.Replace(h, "__COVER_MIND__", dataURL("image/jpeg", coverMindustry), -1)
	h = strings.Replace(h, "__COVER_FACTORIO__", dataURL("image/jpeg", coverFactorio), -1)
	h = strings.Replace(h, "__COVER_7DTD__", dataURL("image/jpeg", cover7DTD), -1)
	h = strings.Replace(h, "__COVER_VRISING__", dataURL("image/jpeg", coverVRising), -1)

	for _, n := range tutorialNames {
		b, err := tutorialFS.ReadFile("tutorial/" + n + ".jpg")
		if err != nil {
			continue
		}
		h = strings.Replace(h, "__TUT"+n+"__", dataURL("image/jpeg", b), 1)
	}

	h = strings.Replace(h, "__THEME__", loadTheme(), 1)

	// 登录状态的**开局快照**：本地有没有令牌、服务器地址是什么。
	//
	// 📌 为什么在拼页面时就填进去、而不是让页面自己去问 Go：
	// 页面问 Go 是**异步**的 ⇒ 会先画出主界面、再跳回登录页，闪一下 ✗
	// 填成 body 上的属性就是同步的：页面画出来时已经是该有的那一页 ✓
	//
	// ⚠️ 用 -1（全部替换）：这俩占位符可能在 HTML 里出现不止一次。
	// 只换第一个的话剩下的会留在页面上（assets_test.go 就是抓这个的）✓
	loggedIn := "0"
	if identity.Token() != "" {
		loggedIn = "1"
	}
	h = strings.Replace(h, "__LOGGED_IN__", loggedIn, -1)
	h = strings.Replace(h, "__LOGIN_BASE__", html.EscapeString(loginBase()), -1)
	return h
}
