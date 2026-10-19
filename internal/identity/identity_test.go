// identity 的测试。
//
// 它管的是「服务器凭什么认得出是同一个人」—— 拉黑功能全靠它 ✓
// 所以钉两件事：**同一台机器每次都拿到同一个 ID** ✓、**格式合法** ✓
//
// 🩸 这个测试会**往数据目录里写东西** ⇒ 先确认数据目录没被用户改到别处
// （改过的话就跳过，免得往人家的真实目录里塞文件 ✗）
package identity

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"p2plink/internal/appdir"
)

func TestRandomIDIsHex64Bit(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{16}$`) // 8 字节 = 64 位
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := randomID()
		if !re.MatchString(id) {
			t.Fatalf("ID 格式不对：%q（该是 16 个十六进制字符）", id)
		}
		if seen[id] {
			t.Fatalf("两次生成撞了同一个 ID：%q —— 随机源有问题", id)
		}
		seen[id] = true
	}
}

func TestClientIDIsStablePerMachine(t *testing.T) {
	if appdir.IsCustom() {
		t.Skip("数据目录被用户改到别处了 —— 这个测试会往那儿写文件，跳过")
	}
	// 用 appdir 的环境变量直接顶掉数据目录 ✓
	// （只顶 APPDATA 已经不够了 —— 自动挑盘会绕过它，写到真实盘上去 ✗）
	t.Setenv(appdir.EnvDir, t.TempDir())

	first := ClientID()
	if first == "" {
		t.Fatal("拿不到 ID")
	}
	// 再拿一次必须是同一个（这就是"服务器认得出同一个人"的基础 ✓）
	if second := ClientID(); second != first {
		t.Fatalf("两次拿到不同 ID：%q vs %q —— 拉黑功能会失效", first, second)
	}
	// 而且真的落盘了（下次启动还在）
	p := filepath.Join(appdir.Dir(), "client-id")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ID 没落盘（%s）：%v", p, err)
	}
	if got := strings.TrimSpace(string(b)); got != first {
		t.Errorf("盘上存的内容不对：%q，该是 %q", got, first)
	}
}
