package main

import "testing"

// 单 IP 的房间数上限。
//
// ⭐ 这是全套限制里**唯一一条用户真正绕不过去的** —— 它在服务器侧，
// 改客户端也碰不到 ✓
//
// 🩸 为什么需要它：别的限制（单 IP 并发连接、每秒新建）都是"连接级"的，
// 一个人完全可以在限制内开一堆房间，把**全局**配额占满 ⇒ 别人开不了 ✗
func TestRoomsPerIP(t *testing.T) {
	const ip = "203.0.113.7"

	// 先把这个 IP 的计数清干净（其它测试可能动过 ✓）
	ipMu.Lock()
	delete(roomsOfIP, ip)
	ipMu.Unlock()

	// ① 前 maxRoomsPerIP 个该过 ✓
	for i := 0; i < maxRoomsPerIP; i++ {
		if !allowRoom(ip) {
			t.Fatalf("第 %d 个房间该放行，却被拒了 ✗", i+1)
		}
	}

	// ② 再开就该拒 ✓
	if allowRoom(ip) {
		t.Errorf("已经开了 %d 个，第 %d 个该被拒 ✗", maxRoomsPerIP, maxRoomsPerIP+1)
	}

	// ③ 🩸 **关掉一个之后，必须能再开一个** ✓✓
	//
	// 这条是防"名额只扣不还"的 —— 漏了 releaseRoom 的话，
	// 用户第一次开房好好的、关了再开就不行了 ✗ 而且这种 bug 特别隐蔽 ✓
	releaseRoom(ip)
	if !allowRoom(ip) {
		t.Error("关掉一个房间后该能再开一个 ✗（说明名额没还回去）")
	}
	releaseRoom(ip)

	// ④ 别的 IP 不受影响 ✓
	const other = "198.51.100.9"
	ipMu.Lock()
	delete(roomsOfIP, other)
	ipMu.Unlock()
	if !allowRoom(other) {
		t.Error("另一个 IP 该能开房，不该被上面那个 IP 连累 ✗")
	}
	releaseRoom(other)

	// ⑤ 多还几次也不会把计数还成负数 / 把别人的份额还掉 ✓
	releaseRoom(other)
	releaseRoom(other)
	ipMu.Lock()
	n := roomsOfIP[other]
	ipMu.Unlock()
	if n != 0 {
		t.Errorf("还多了之后该归零（甚至删掉），实得 %d ✗", n)
	}

	// ⑥ 归零时那张表里该**没有这个键**了 —— 不然它会无界地长 ✓
	ipMu.Lock()
	_, stillThere := roomsOfIP[other]
	ipMu.Unlock()
	if stillThere {
		t.Error("计数归零后该把键删掉，不然这张表会一直长 ✗")
	}
}
