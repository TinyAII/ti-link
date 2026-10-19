// 「朋友重发新候选」这条路的自检。
//
// 🩸 钉的是一个**真故障**（2026-10-18 从老板的房主日志里发现的）：
//
//	日志里连续两条
//	  「收到「玩家 X」重发的新候选 」   ← 空的
//	  「重协商：收到对方的新候选 」    ← 还是空的
//	紧接着「打洞失败：punch: 没有可用的候选地址」
//
// 根因：服务端转发时把候选填在 Cands，而**房主读的是 m.Peer** ⇒ 房主永远
// 拿到空列表 ⇒ 重协商第二轮必挂。
//
// 协议里这两个字段的分工是「谁的地址」：
//
//	Cands = 发这条消息的人**自己**的候选
//	Peer  = **对端**的候选
//
// 朋友发上来用 Cands（自己是发送方），转发给房主时从房主角度看就是"对端" ⇒
// 必须是 Peer。填错了**编译期发现不了、运行期也不报错**，只是"第二轮打不通" ✗
package main

import (
	"testing"

	"p2plink/internal/proto"
)

// 反证方法：把 cmd/signal/join.go 里那句 `Peer: cands` 改回 `Cands: cands`
// ⇒ 这个测试立刻红（候选是空的）✓
func TestReCandReachesHost(t *testing.T) {
	addr := startTestServer(t)
	const room = "T-RECAND"

	host := dialT(t, addr)
	send(t, host, proto.Msg{
		Type: proto.TypeRegister, Role: "host", Game: "我的世界", Room: room,
		ClientID: "cid-host", Label: nickA, MaxSize: 4, PubKey: newPub(t),
	})
	recvUntil(t, host, proto.TypeRoomOK)

	guest := dialT(t, addr)
	send(t, guest, proto.Msg{
		Type: proto.TypeRegister, Role: "join", Game: "我的世界", Room: room,
		ClientID: "cid-guest", Label: nickB, Cands: []string{"10.0.0.1:5000"},
		PubKey: newPub(t),
	})

	// 先拿到 pair，才知道 TargetID 该是哪个
	pair := recvUntil(t, host, proto.TypePair)

	// 朋友重开了本地端口，把新候选报上来
	want := []string{"120.228.1.2:40001", "192.168.1.9:40001"}
	send(t, guest, proto.Msg{Type: proto.TypeReCand, Cands: want})

	got := recvUntil(t, host, proto.TypeReCand)
	if got.TargetID != pair.TargetID {
		t.Errorf("TargetID 对不上：%q，pair 里是 %q —— 房主会找不到是哪位朋友",
			got.TargetID, pair.TargetID)
	}
	if len(got.Peer) == 0 {
		t.Fatalf("房主收到的候选是空的 —— 服务端填错字段了？(Peer=%v Cands=%v)",
			got.Peer, got.Cands)
	}
	if len(got.Peer) != len(want) {
		t.Fatalf("候选条数不对：%v，想要 %v", got.Peer, want)
	}
	for i := range want {
		if got.Peer[i] != want[i] {
			t.Errorf("第 %d 条候选不对：%q，想要 %q", i, got.Peer[i], want[i])
		}
	}
}

// 服务端得把**空候选**挡在外面：否则房主那边会白跑一轮"没有可用的候选地址"。
//
// 📌 空列表本身不算非法（validateCands 只卡条数和长度），所以现在会照转。
// 这个测试只是把**现状**钉住 —— 哪天想让服务端直接丢弃空候选，改的是
// validateCands，届时这条会红，提醒"这里的行为变了" ✓
func TestEmptyReCandIsForwardedAsIs(t *testing.T) {
	addr := startTestServer(t)
	const room = "T-RECAND0"

	host := dialT(t, addr)
	send(t, host, proto.Msg{
		Type: proto.TypeRegister, Role: "host", Game: "我的世界", Room: room,
		ClientID: "cid-host", Label: nickA, MaxSize: 4, PubKey: newPub(t),
	})
	recvUntil(t, host, proto.TypeRoomOK)

	guest := dialT(t, addr)
	send(t, guest, proto.Msg{
		Type: proto.TypeRegister, Role: "join", Game: "我的世界", Room: room,
		ClientID: "cid-guest", Label: nickB, Cands: []string{"10.0.0.1:5000"},
		PubKey: newPub(t),
	})
	recvUntil(t, host, proto.TypePair)

	send(t, guest, proto.Msg{Type: proto.TypeReCand}) // 故意不带候选
	got := recvUntil(t, host, proto.TypeReCand)
	if len(got.Peer) != 0 {
		t.Errorf("空候选被转成了 %v —— 服务端不该凭空造地址", got.Peer)
	}
}
