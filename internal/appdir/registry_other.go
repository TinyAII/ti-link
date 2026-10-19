// 非 Windows 平台的空实现：数据目录就是系统默认那个，改不了。
//
// 这套东西实际只在 Windows 客户端上用（GUI 本来就是 Windows 专用的），
// 留这个文件只是为了**别的平台也能编译过** —— 不然交叉编译 / 跑测试就会卡在这儿 ✗
//go:build !windows

package appdir

func init() {
	readParent = func() string { return "" }
	writeParent = func(string) error { return nil }
}
