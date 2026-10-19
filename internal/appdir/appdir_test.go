// appdir 的测试。
//
// 🩸 为什么那两个读写口子是**变量**而不是函数：就为了这里能顶掉它们 ✗
// 不然跑一次测试就往用户的 HKCU 里写东西（还会把真实的数据目录指到别处）——
// 那比测试本身危险多了 ✓
package appdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stub 把"注册表"和"硬盘探测"两个口子都换成假的，测完还原。
//
// 🩸 两个都得顶掉：只顶注册表的话，这些测试就会**依赖跑测试的机器** ——
// 有 D 盘的机器会挑 D 盘、没有的就回 AppData ⇒ 同一份代码两种结果 ✗
// （一开始只顶了注册表，在真实机器上跑就露馅了 ✓）
func stub(t *testing.T) *string {
	t.Helper()
	// 连系统默认目录也指到临时目录 —— 不然 hasData(AppData) 会看**真实**的
	// %AppData%\p2plink，那又变成"看这台机器装没装过"了 ✗
	t.Setenv("APPDATA", t.TempDir())

	var cur string
	oldR, oldW, oldD := readParent, writeParent, listUsableDrives
	readParent = func() string { return cur }
	writeParent = func(s string) error { cur = s; return nil }
	listUsableDrives = func() []string { return nil }
	t.Cleanup(func() { readParent, writeParent, listUsableDrives = oldR, oldW, oldD })
	return &cur
}

// 没设过 ⇒ 用系统默认，而且**跟老版本完全一样**（AppData\p2plink）。
// 这条最要紧：老用户升级上来，数据目录不能变 ✗
func TestDefaultIsUnchanged(t *testing.T) {
	stub(t)
	want := filepath.Join(DefaultParent(), "p2plink")
	if got := Dir(); got != want {
		t.Fatalf("默认目录该是 %q，实得 %q —— 老用户的数据要留在这儿", want, got)
	}
	if IsCustom() {
		t.Error("没设过的时候不该算「用户改过」")
	}
}

// 用户选的是**父目录**，文件落在它下面我们那层子目录里 —— 不撒在人家文件夹根上。
func TestCustomParentGetsOwnSubdir(t *testing.T) {
	stub(t)
	if err := SetParent(`D:\我的资料`); err != nil {
		t.Fatal(err)
	}
	if got, want := Dir(), filepath.Join(`D:\我的资料`, "p2plink"); got != want {
		t.Errorf("该落在 %q，实得 %q", want, got)
	}
	if !IsCustom() {
		t.Error("设过之后该算「用户改过」")
	}
}

// 恢复默认：传空串 ⇒ 值被清掉，回到系统默认。
func TestResetToDefault(t *testing.T) {
	stub(t)
	_ = SetParent(`D:\别的`)
	if err := SetParent(""); err != nil {
		t.Fatal(err)
	}
	if IsCustom() {
		t.Error("恢复默认之后不该还算「用户改过」")
	}
	if !strings.HasPrefix(Dir(), DefaultParent()) {
		t.Errorf("恢复默认之后该回到系统默认目录下，实得 %q", Dir())
	}
}

// 父目录写了个空白的（用户手滑 / 配置坏了）⇒ 当没设过处理，别把数据目录指到奇怪的地方。
func TestBlankParentFallsBackToDefault(t *testing.T) {
	cur := stub(t)
	*cur = "   "
	// 🩸 必须把"别的盘"也顶掉：不顶的话这条会**依赖跑测试的机器**
	// （有 D 盘的机器会挑 D 盘 ⇒ 断言就红了 ✗ —— 一开始就是这毛病，已修）
	old := listUsableDrives
	listUsableDrives = func() []string { return nil }
	t.Cleanup(func() { listUsableDrives = old })

	if IsCustom() {
		t.Error("只有空白的父目录该当没设过")
	}
	if got, want := Dir(), filepath.Join(DefaultParent(), "p2plink"); got != want {
		t.Errorf("该退回默认 %q，实得 %q", want, got)
	}
}

// ---- 数据目录规则（2026-10-16 晚改：**不再优先别的固定盘**）----
//
// chooseParent 是纯函数 ⇒ 不用碰硬盘、也不用碰注册表就能把分支钉死 ✓
//
// 🩸 反证方法：把 `if overridden != ""` 那句删掉 ⇒ 立刻红 ✓
//
//	另一条（"不许再挑别的盘"）的反证：把 otherDrive 那档加回来 ⇒ 也红 ✓
func TestChooseParentOnlyAppData(t *testing.T) {
	const appData = `C:\Users\x\AppData\Roaming`

	// 默认走 %AppData%（每用户目录）—— **不再挑别的盘**
	// （实测 D:\ 的 ACL 让所有本地用户可写；界面 HTML 落那儿会被改写 ✗）
	if got := chooseParent("", appData); got != appData {
		t.Errorf("默认该用 AppData（每用户目录），实得 %q ✗（别再挑别的盘了）", got)
	}
	// 用户在设置里指定过 ⇒ 听他的（哪怕他就是要放别处 ✓）
	if got := chooseParent(`E:\我的东西`, appData); got != `E:\我的东西` {
		t.Errorf("指定过的该优先，实得 %q", got)
	}
}

// hasData：空目录不算"有数据"（不然一个空文件夹就能把自动挑盘废掉 ✗）
func TestHasData(t *testing.T) {
	dir := t.TempDir()
	if hasData(dir) {
		t.Error("空目录不该算有数据")
	}
	if err := os.MkdirAll(filepath.Join(dir, "子目录"), 0o755); err != nil {
		t.Fatal(err)
	}
	if hasData(dir) {
		t.Error("只有子目录、没有文件，也不算有数据")
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasData(dir) {
		t.Error("有文件了就该算有数据")
	}
}

// 真机器上挑出来的盘必须**不是 C 盘**，而且形如 "X:\"。
// （挑不到就跳过 —— 有些机器只有一个盘 ✓）
func TestPickOtherDriveSkipsC(t *testing.T) {
	d := pickOtherDrive()
	if d == "" {
		t.Skip("这台机器没有别的固定盘")
	}
	if len(d) != 3 || d[1] != ':' || d[2] != '\\' {
		t.Errorf("盘符形状不对：%q", d)
	}
	if d[0] == 'C' || d[0] == 'c' {
		t.Errorf("不该挑 C 盘：%q", d)
	}
}
