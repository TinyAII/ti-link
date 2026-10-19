// 用户设置的读写：统一存在 %AppData%\p2plink\settings.json。
package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"p2plink/internal/appdir"
)

// appVersion 当前版本号。关于页和「检测更新」都用它。
const appVersion = "1.0.1"

// Settings 就是设置页里的那些开关。
type Settings struct {
	Theme     string `json:"theme"`     // light | dark
	AutoStart bool   `json:"autoStart"` // 开机时运行
	// MinimizeToTray 已经不用了 —— 改成"关窗口时弹窗问"，见 closechoice.go。
	// 🩸 **字段特意留着不删**：删了就等于动了 settings.json 的格式（老配置里那行会被丢掉）。
	// 留着的话老配置照读，只是没人再看它。
	MinimizeToTray   bool `json:"minimizeToTray"`
	CheckUpdateStart bool `json:"checkUpdateOnStart"` // 启动时检查更新（占位）
	RememberPort     bool `json:"rememberPort"`       // 记住**每个游戏各自**的端口
	// LastPort 是**旧字段**：原来只有一个"上次用过的端口"，跨游戏串着用 ——
	// 选了泰拉瑞亚、框里却是我上次我的世界填的数字，说不通 ✗
	// 🩸 留着不删：删了就等于动了 settings.json 的格式（老配置那行会被丢掉）✓
	LastPort string `json:"lastPort"`
	// PortByGame 记「每个游戏上次用过的端口」，比如 {"泰拉瑞亚": "7777"} ✓
	// omitempty 很要紧：老配置里没这个字段 ⇒ 读进来是 nil ⇒ 当作"一个都没记过" ✓
	PortByGame     map[string]string `json:"portByGame,omitempty"`
	SoundOnConnect bool              `json:"soundOnConnect"` // 连接成功时提示音
	Nick           string            `json:"nick"`           // 昵称（建房/加房时报给服务器）
	// CloseAction **决定关窗口时到底做什么**：
	//
	//	"ask"  → 弹窗问一次（弹窗勾「不再提醒」会把答案写回来 ⇒ 以后就不问了）
	//	"tray" → 直接缩到托盘
	//	"quit" → 直接退出
	//
	// 🩸 关窗那条路（main.go 的 SetOnCloseIntercept）**只读这个值** ✓
	// 早先它只读"本次开机记住的"、不看设置 ⇒ 在设置里改成"退出主程序"也没用 ✗（犯过 ✓）
	CloseAction string `json:"closeAction"`
	// NickSet 记「昵称这件事用户表过态了没有」。
	// 用它区分两种空昵称：从没设过（首次运行 → 自动摇一个默认名）
	// vs 用户主动清空（那就真的不要名字，别再偷偷给他摇回来）。
	NickSet bool `json:"nickSet"`
	// StunServers 自定义 STUN 服务器（逗号分隔）。空 = 用内置默认值。
	//
	// 什么时候会想改：内置的三个在某些网络下都不通（罕见，但出现过），
	// 或者自己架了一个 STUN。**普通用户不用动它** ✓
	// 🩸 空串 = 用默认值，不能理解成"一个都不要" ✗
	StunServers string `json:"stunServers,omitempty"`
	// Avatar 是内置头像库的下标（**存的是数字，不是图** —— 图库内置在 exe 里）。
	// AvatarSet 跟 NickSet 一个意思：区分「从没设过」（首次运行随机分一张）
	// 和「用户自己挑过了」。
	Avatar    int  `json:"avatar"`
	AvatarSet bool `json:"avatarSet"`
	// LoginBase 是**登录服务器**地址（管理面板那台）。
	//
	// 空 = 用内置默认值（internal/loginapi.DefaultBase）。
	// 📌 为什么让用户能改：服务器地址是最可能变的东西（换机器、换端口）。
	// 写死的话一换就得重发一遍客户端 ✗
	// 跟"客户端连的是域名不是 IP"是同一个思路 —— 都是"别把易变的东西钉死在 exe 里"✓
	LoginBase string `json:"loginBase,omitempty"`
	// 注意：房间密码不存 —— 那是房主设的、一次性的东西，不该落在盘上。
	// 「记忆房间 ID」也不存：服务器上的房间是临时的，下次开出来就是新 ID，记了没用。
	// 🩸 **登录令牌也不在这儿** —— 它是凭据，单独存在数据目录的 token 文件里
	// （见 internal/identity/token.go）。混进 settings.json 的话，
	// 一次"恢复默认设置"就可能把它抹掉、或者跟着备份泄出去 ✗
}

// defaultSettings 是「恢复默认设置」还原成的样子，也是首次运行时的初值。
func defaultSettings() Settings {
	return Settings{
		CheckUpdateStart: true,
		RememberPort:     true,
		// 连上响一声，默认开着 —— 联机时人常常切在游戏窗口里，
		// 听声比回头看屏幕靠谱。
		SoundOnConnect: true,
		// 默认「每次询问」：新用户第一次关窗会被问一次，从而知道有这个功能 ✓
		// 选过固定的之后就不再打扰 ✓
		CloseAction: "ask",
		LastPort:    "25565",
	}
}

var (
	settingsMu sync.Mutex
	settingsC  *Settings // 缓存，免得每次读盘
)

// configDir 数据目录（可以被动过，见 internal/appdir）。
// 📌 全项目就这一处读它 —— identity 那份 client-id 也走同一个地方，
// 不然搬了家会出现"设置跟着换了、身份还留在老地方" ✗
func configDir() string { return appdir.Dir() }

func settingsPath() string {
	d := configDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "settings.json")
}

// loadSettings 读设置；文件不存在或者坏了就退回默认值。
func loadSettings() Settings {
	s := defaultSettings()
	p := settingsPath()
	if p == "" {
		return s
	}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s.LastPort == "" {
		s.LastPort = "25565"
	}
	return s
}

// currentSettings 取当前生效的设置。
func currentSettings() Settings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	if settingsC == nil {
		s := loadSettings()
		settingsC = &s
	}
	return *settingsC
}

// updateSettings 改设置并立刻落盘。
func updateSettings(fn func(*Settings)) error {
	settingsMu.Lock()
	if settingsC == nil {
		s := loadSettings()
		settingsC = &s
	}
	fn(settingsC)
	snapshot := *settingsC
	settingsMu.Unlock()
	return writeSettings(snapshot)
}

func writeSettings(s Settings) error {
	p := settingsPath()
	if p == "" {
		return errors.New("找不到用户配置目录")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// 先写临时文件再改名：写一半被中断也不会把配置弄坏
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// openConfigDir 用资源管理器打开配置目录（排查问题时很有用）。
func openConfigDir() error {
	d := configDir()
	if d == "" {
		return errors.New("找不到用户配置目录")
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	return exec.Command("explorer", d).Start()
}

// legacyThemePath 是 v1.0.0 之前单独存主题的老位置，读的时候兼容一下。
func legacyThemePath() string {
	d := configDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "theme")
}

// loadTheme 返回 light 或 dark。
func loadTheme() string {
	if s := currentSettings(); s.Theme == "light" || s.Theme == "dark" {
		return s.Theme
	}
	// 兼容旧版单独存的 theme 文件
	if p := legacyThemePath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			if t := strings.TrimSpace(string(b)); t == "light" || t == "dark" {
				return t
			}
		}
	}
	return "dark"
}

func saveTheme(name string) {
	if name != "light" && name != "dark" {
		return
	}
	_ = updateSettings(func(s *Settings) { s.Theme = name })
}
