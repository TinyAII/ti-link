// 暴露给设置页的那几个方法。
// 存储和平台实现分别在 settings.go / settings_windows.go，这里只做「页面 → 设置」的翻译。
package main

import (
	"net"
	"strconv"
	"strings"

	"p2plink/internal/appdir"
	"p2plink/internal/netutil"
)

// SaveTheme 由页面调用：记住用户选的深浅色。
func (b *backend) SaveTheme(name string) {
	saveTheme(name)
	setWindowTheme(b.hwnd, name == "dark") // 窗口圆角描边跟着一起换
}

// GetSettings 由页面调用：一次性把所有设置取过去。
// 开机自启以注册表为准 —— 用户可能在别的地方改过，注册表才是真相。
func (b *backend) GetSettings() map[string]any {
	s := currentSettings()
	return map[string]any{
		"autoStart":          autoStartEnabled(),
		"checkUpdateOnStart": s.CheckUpdateStart,
		"rememberPort":       s.RememberPort,
		"lastPort":           s.LastPort,
		// 每个游戏各自记住的端口 {游戏名: 端口}。老配置里没有 ⇒ nil ⇒ 前端当空对象 ✓
		"portByGame":     s.PortByGame,
		"soundOnConnect": s.SoundOnConnect,
		"theme":          loadTheme(),
		"nick":           s.Nick,
		"nickSet":        s.NickSet,
		"avatar":         s.Avatar,
		"avatarSet":      s.AvatarSet,
		"avatarCount":    AvatarCount(),
		"version":        appVersion,
		"configDir":      configDir(),
		// customDir：数据目录是不是用户自己改过（设置页据此显示「恢复默认」✓）
		"customDir":   appdir.IsCustom(),
		"closeAction": s.CloseAction,
		// STUN 列表：返回用户自定义值 + 内置默认值（界面用它显示占位提示和「恢复默认」）
		"stunServers": s.StunServers,
		"stunDefault": netutil.DefaultStunServers,
	}
}

// SetSetting 由页面调用：改一个设置项。返回错误文案，空字符串表示成功。
func (b *backend) SetSetting(key, value string) string {
	on := value == "true"

	// 开机自启要写注册表，单独走
	if key == "autoStart" {
		if err := setAutoStart(on); err != nil {
			return "设置开机自启失败：" + err.Error()
		}
		return ""
	}

	var err error
	switch key {
	case "checkUpdateOnStart":
		err = updateSettings(func(s *Settings) { s.CheckUpdateStart = on })
	case "rememberPort":
		err = updateSettings(func(s *Settings) { s.RememberPort = on })
	case "soundOnConnect":
		err = updateSettings(func(s *Settings) { s.SoundOnConnect = on })
	case "closeAction":
		// 它是"选项"不是开关 ⇒ 值要校验，别让页面塞个乱字符串进来
		v := strings.TrimSpace(value)
		if v != "ask" && v != "tray" && v != "quit" {
			return "不认识的选项：" + value
		}
		err = updateSettings(func(s *Settings) { s.CloseAction = v })
	case "stunServers":
		// 空串 = 恢复默认（不是"一个都不用"）✓
		v := strings.TrimSpace(value)
		if v != "" {
			if msg := checkStunList(v); msg != "" {
				return msg
			}
		}
		err = updateSettings(func(s *Settings) { s.StunServers = v })
	case "nick":
		// 昵称不是开关，是个人信息页存的一串字；顺手按跟后端一样的规则收干净。
		// 同时打上 NickSet —— 存过（哪怕存的是空串）就算用户表过态了，
		// 以后启动不再自动塞默认名进去。
		err = updateSettings(func(s *Settings) { s.Nick = cleanNick(value); s.NickSet = true })
	case "avatar":
		// 下标是页面上点的，收进来之前卡到有效范围 ——
		// 越界值一旦存下去，之后每局都会按它去查别人的图库，有可能查到别人的脸上
		n, convErr := strconv.Atoi(value)
		if convErr != nil || n < 0 || n >= AvatarCount() {
			return "头像编号超出范围"
		}
		err = updateSettings(func(s *Settings) { s.Avatar = n; s.AvatarSet = true })
	default:
		return "未知的设置项"
	}
	if err != nil {
		return "保存设置失败：" + err.Error()
	}
	return ""
}

// CheckUpdate 由页面调用：检查更新。返回给用户看的一行文案。
//
// 🩸 拉不到的时候**直说"检查失败"**，别顺手回一句"已是最新" ——
// 那会让人以为自己的版本没问题，真出了要紧的更新他反而收不到 ✗
func (b *backend) CheckUpdate() string {
	info, err := fetchLatest()
	if err != nil {
		return "检查更新失败，请稍后再试"
	}
	if !newerThan(appVersion, info.Version) {
		setPendingUpdate(nil)
		return "当前已是最新版本 v" + appVersion
	}
	setPendingUpdate(info)
	return "发现新版本 v" + info.Version + "（当前 v" + appVersion + "）"
}

// UpdateInfo 给页面：待更新的版本号 / 下载地址 / 说明。
// 没有待更新就返回空 map ✓（手动点「检测更新」之后，页面靠它决定弹不弹窗）
func (b *backend) UpdateInfo() map[string]any {
	info := pendingUpdate()
	if info == nil {
		return map[string]any{}
	}
	return updateInfoMap(info)
}

// TakeStartupUpdate 由页面在初始化时调用：**取走**启动时静默检查的结果。
//
// 只在第一次返回东西（见 takeStartupUpdate 的注释），取过之后就是空的 ——
// 所以"启动时查到新版本"这件事一次启动只会弹一个窗 ✓
func (b *backend) TakeStartupUpdate() map[string]any {
	info := takeStartupUpdate()
	if info == nil {
		return map[string]any{}
	}
	return updateInfoMap(info)
}

func updateInfoMap(info *updateInfo) map[string]any {
	return map[string]any{
		"version": info.Version,
		"url":     info.URL,
		"note":    info.Note,
	}
}

// OpenDownloadPage 由页面调用：打开待下载的更新页。返回错误文案，空 = 成功。
//
// 📌 现在的下载地址就是官网（latest.json 里 url = https://ti.tinyaii.cn/）。
// url 空着时退到官网首页 —— 见 officialSite 那段注释：
// 宁可多送一次首页，也别给用户一个"有新版本但没处点"的弹窗 ✗
func (b *backend) OpenDownloadPage() string {
	info := pendingUpdate()
	if info == nil {
		return "还没有可下载的新版本"
	}
	u := info.URL
	if u == "" {
		u = officialSite
	}
	if err := openURL(u); err != nil {
		return err.Error()
	}
	return ""
}

// OpenConfigDir 由页面调用：打开配置文件夹。返回错误文案，空 = 成功。
func (b *backend) OpenConfigDir() string {
	if err := openConfigDir(); err != nil {
		return "打开失败：" + err.Error()
	}
	return ""
}

// ResetSettings 由页面调用：恢复默认设置。
// 主题和昵称不动 —— 主题是怕用户正看着界面一下给换了个样；
// 昵称是用户自己打的字，不属于「开关」，抹掉纯属添乱。
func (b *backend) ResetSettings() string {
	if err := setAutoStart(false); err != nil {
		return "关闭开机自启失败：" + err.Error()
	}
	cur := currentSettings()
	def := defaultSettings()
	def.Theme = cur.Theme
	def.Nick = cur.Nick
	def.Avatar = cur.Avatar
	def.AvatarSet = cur.AvatarSet
	def.NickSet = cur.NickSet
	if err := writeSettings(def); err != nil {
		return "保存失败：" + err.Error()
	}
	settingsMu.Lock()
	settingsC = &def
	settingsMu.Unlock()
	return ""
}

// allowedLinkPrefixes 是 OpenLink 肯放行的前缀。
//
// 📌 每个站点单独列，不做通配（`*.bilibili.com` 这种一旦扩大就难收回）✓
// 📌 一律要求 https —— 明文链接能被中间人换成任意内容（跟更新源那条同一个道理）✓
var allowedLinkPrefixes = []string{
	"https://ti.tinyaii.cn/", // 官网（关于作者页最上面那条）—— 更新检查也指向它
	"https://qm.qq.com/q/",   // QQ 群邀请链接（关于作者页那几个）
	"https://b23.tv/",
	"https://space.bilibili.com/",
	"https://www.bilibili.com/",
}

// linkAllowed 判断页面传来的链接在不在白名单里。
//
// 🩸 单独抽出来是为了**能测**：它原来埋在 OpenLink 里，
// 而 OpenLink 一调就真去开系统浏览器 —— 测不了 ✗
// 这恰恰是这段代码里最该测的部分（判错就等于开了个"点一下打开任意网站"的洞）✓
func linkAllowed(u string) bool {
	for _, pre := range allowedLinkPrefixes {
		if strings.HasPrefix(u, pre) {
			return true
		}
	}
	return false
}

// OpenLink 由页面调用：用系统浏览器打开一个链接。
//
// 🩸🩸 **必须限制域名**：这个函数是给页面调的，而页面会渲染用户可控的内容
//
//	（昵称、房间名…）⇒ 不加限制的话，以后哪天真有人把用户输入拼进 href，
//	就能变成"点一下打开任意网站" ✗（钓鱼链接、恶意下载页）
//	⇒ 只放行作者自己的几个主页，别的一律拒 ✓
func (b *backend) OpenLink(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return "链接是空的"
	}
	if !linkAllowed(u) {
		return "这个链接不在允许范围内"
	}
	if err := openURL(u); err != nil {
		return "打不开浏览器：" + err.Error()
	}
	return ""
}

// checkStunList 校验用户填的 STUN 列表。返回空串 = 通过。
//
// 为什么要校验：STUN 不通时要**白等 2.4 秒**（2 次 × 1.2 秒）。
// 让用户随手粘个网址进去，等于每次探测都多等几秒，比不改还慢 ✗
//
// 判据：必须是 host:port 形状、端口是数字。**不校验域名能不能解析** ——
// 那要联网，而且用户可能填的就是等会儿才能用的地址 ✓
func checkStunList(v string) string {
	list := netutil.SplitList(v)
	if len(list) > maxStunServers {
		return "最多填 " + strconv.Itoa(maxStunServers) + " 个（每个不通的都要白等 2.4 秒）"
	}
	for _, item := range list {
		host, port, err := net.SplitHostPort(item)
		if err != nil || strings.TrimSpace(host) == "" {
			return "「" + item + "」不是 host:port 的格式（例：stun.miwifi.com:3478）"
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "「" + item + "」的端口不对"
		}
	}
	return ""
}

// maxStunServers 最多允许几个自定义 STUN（多了纯属白等）。
const maxStunServers = 6
