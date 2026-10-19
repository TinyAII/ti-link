// 房间：朋友侧。
//
// 加入要过四关（房间密码 / 黑名单 / 房主还在 / 没满员），过了就登记进名单、
// 请房主为 TA 打洞，然后就这么挂着等断开。
package main

import (
	"log"
	"strconv"
	"time"

	"p2plink/internal/auth"
	"p2plink/internal/proto"
	"p2plink/internal/signal"
)

// owner 是这位访客登录的账号（鉴权那一步已经查出来了，原来直接丢掉了）。
// 📌 历史记录要它：**昵称随时能改，账号才是身份** ——
//
//	后台排查"这个人什么时候进过房"时，靠昵称根本对不上 ✓
func serveJoin(cl *signal.Client, cc *countingConn, maskedRemote, ip string, m proto.Msg,
	owner *auth.Account) {
	roomsMu.Lock()
	r, ok := rooms[m.Room]
	roomsMu.Unlock()
	if !ok {
		cl.Send(proto.Msg{Type: proto.TypeError, Err: "房间不存在，检查一下房间 ID"})
		return
	}

	r.mu.Lock()

	// 🩸 **先看有没有被"密码错太多次"锁着** —— 锁着的时候连密码都不用比 ✓
	// （不然等于还让他用 CPU 换信息，而且他迟早会猜中 ✗）
	// 规则见 passfail.go：错 3 次锁 30 秒、翻倍、上限 10 分钟 ✓
	if wait := passBlockedLocked(r, ip, time.Now()); wait > 0 {
		r.mu.Unlock()
		log.Printf("房间 %s：%s 被密码退避锁着（还有 %s 才放行），拒绝加入",
			m.Room, maskedRemote, wait.Round(time.Second))
		cl.Send(proto.Msg{Type: proto.TypeError,
			Err: "房间密码错太多次了，先等" + humanWait(wait) + "再试"})
		return
	}

	// 密码这道关卡放在最前面：没进得来的人，不该从服务器这儿问出
	// 「这个房间在不在、满没满、自己有没有被拉黑」这些信息。
	if !passEqual(m.RoomPass, r.pass) {
		// 📌 记账要在**持锁期间**做（函数名带 Locked）⇒ 先算再解锁 ✓
		lock := notePassFailLocked(r, ip, time.Now())
		r.mu.Unlock()
		if lock > 0 {
			log.Printf("房间 %s：%s 房间密码连续错到阈值，锁 %s",
				m.Room, maskedRemote, lock.Round(time.Second))
			cl.Send(proto.Msg{Type: proto.TypeError,
				Err: "房间密码错太多次了，先等" + humanWait(lock) + "再试"})
			return
		}
		log.Printf("房间 %s：%s 房间密码不对，拒绝加入", m.Room, maskedRemote)
		cl.Send(proto.Msg{Type: proto.TypeError, Err: "房间密码不对"})
		return
	}
	// 密码对了 ⇒ **整笔清零** ✓（不然手滑过两次的人，下次一错就被锁 ✓ 那种体验很糟）
	clearPassFailLocked(r, ip)

	if b := r.findBanLocked(m.ClientID, ip); b != nil {
		r.mu.Unlock()
		statRejectBan.Add(1)
		log.Printf("房间 %s：%s 在黑名单中（登记为 %s），拒绝加入", m.Room, maskedRemote, b.label)
		cl.Send(proto.Msg{Type: proto.TypeBanned, Notice: "房主已经将你拉黑，你不可再进入该房间"})
		return
	}

	host := r.host
	if host == nil {
		r.mu.Unlock()
		cl.Send(proto.Msg{Type: proto.TypeError, Err: "房主已离开"})
		return
	}
	if 1+len(r.joins) >= r.maxSize {
		r.mu.Unlock()
		cl.Send(proto.Msg{Type: proto.TypeError, Err: "房间已满"})
		return
	}

	// 临时密钥协商的两个前提。
	//
	// 🩸 这儿必须给一句人话：两端公钥对不上时，表现是"打洞成功但握手静默失败"——
	// 用户只看到卡住不动，根本猜不到是版本不一致。宁可现在就说清楚。
	// 🩸 游戏对不上 ⇒ **不让进**（老板要求：不一样的房间不可加入）✓
	// 放在最前面：别先做一堆准备工作再拒 ✗
	// 判据本身（含"没报就放行"那条边界）见 rooms.go 的 gameMismatchRefusal ✓
	joinerGame := sanitizeGame(m.Game)
	// 🩸 两边都得报、而且必须一样；**没报也拒**（不为老客户端留口子）✗
	if why := refuseJoinGame(host.game, joinerGame); why != "" {
		r.mu.Unlock()
		log.Printf("房间 %s：拒绝了 %s 的加入 —— 他报的是「%s」，房主开的是「%s」",
			m.Room, maskedRemote, joinerGame, host.game)
		cl.Send(proto.Msg{Type: proto.TypeError, Err: why})
		return
	}

	if len(m.PubKey) == 0 {
		r.mu.Unlock()
		cl.Send(proto.Msg{Type: proto.TypeError,
			Err: "你这边的客户端版本太旧，请更新到最新版再联机"})
		return
	}
	hostPub := host.pub
	if len(hostPub) == 0 {
		r.mu.Unlock()
		cl.Send(proto.Msg{Type: proto.TypeError,
			Err: "房主的客户端版本太旧，让 TA 也更新到最新版"})
		return
	}

	r.seq++
	// 昵称只存纯名字（没起名就是空串，界面拿 seat 拼「玩家 N」）；
	// 先清洗（超长截断 + 不在词库里的当没起名，见 sanitizeLabel），
	// 重名再加个序号，免得名单里两行一模一样的「小明」
	label := r.uniqueLabelLocked(sanitizeLabel(m.Label))
	j := &member{
		id:       m.ClientID + "-" + strconv.Itoa(r.seq),
		cid:      m.ClientID,
		label:    label,
		seat:     r.nextSeatLocked(),
		avatar:   m.Avatar,
		pub:      m.PubKey,
		cl:       cl,
		remoteIP: ip,
		game:     joinerGame,
		joinedAt: time.Now(),
		counting: cc,
	}
	r.joins = append(r.joins, j)
	r.broadcastMembersLocked()
	r.mu.Unlock()

	// 历史：谁进来了（房主那条在 serveHost 里补过）
	var acct int64
	if owner != nil {
		acct = owner.ID
	}
	histRecord(histEvent{Kind: histJoin, Room: r.code, Acct: acct,
		CID: j.cid, Label: j.label, Seat: j.seat})

	// 把房主的临时公钥交给朋友。
	// 必须**在打洞之前**送到 —— 隧道密钥要先算出来，连打洞探测包都要拿它签名。
	cl.Send(proto.Msg{Type: proto.TypeHostKey, PubKey: hostPub})

	log.Printf("房间 %s：%s 加入（%s）", m.Room, dispName(j.label, j.seat), maskedRemote)
	// 名字不在词库里 → 上面已经按"没起名"处理了。只说"有这回事"，
	// ⚠️ **不把那个名字写进日志** ✗ —— 那正是要避免的东西。
	// 放这儿记（解锁之后）：在锁里写日志等于把 IO 圈进临界区。
	if m.Label != "" && j.label == "" {
		log.Printf("房间 %s：来自 %s 的名字不在词库里，按未起名处理", m.Room, maskedRemote)
	}

	// 请房主为这位朋友准备一条隧道
	host.cl.Send(proto.Msg{Type: proto.TypePair, TargetID: j.id, Label: label, Seat: j.seat, Peer: m.Cands, PubKey: j.pub})

	// 挂着等断开。朋友主动退出会先发一条 leave（TypeLeave）——
	// 这是**唯一**能区分「主动退出」和「直接关掉程序」的信号：
	// 两种断开在 TCP 层面长得一模一样，连接本身分辨不出来。
	left := false
	for {
		in, err := cl.Recv(0)
		if err != nil {
			break
		}
		switch in.Type {
		case proto.TypeLeave:
			left = true
		case proto.TypeReCand:
			// 加入方重开了本地端口（打洞失败后的重协商），把新候选转给房主 ✓
			forwardJoinCands(r, j, in.Cands)
		}
	}

	r.mu.Lock()
	found := false
	for i, x := range r.joins {
		if x == j {
			r.joins = append(r.joins[:i], r.joins[i+1:]...)
			found = true
			break
		}
	}
	r.broadcastMembersLocked()
	r.mu.Unlock()

	// found=false 说明这人早就不在名单里了（被踢 / 被拉黑时 removeMember 已经摘掉）。
	// 那种情况**既不通知房主也不打日志** —— 踢人时服务器已经记过一条「被踢出」，
	// 再补一句「非正常退出」是重复且误导的。
	if !found {
		return
	}
	// 历史：正常退出 / 掉线。
	// 📌 被踢、被拉黑**不走这条路**（removeMember 已经记过一条带原因的了）——
	// 两边都记会变成"离开两次"，后台看着像来了两个人 ✗
	why := "gone"
	if left {
		why = "left"
	}
	histRecord(histEvent{Kind: histLeave, Room: m.Room, CID: j.cid, Why: why})

	notifyHostLeft(r, j, left)
	kind := "非正常退出"
	if left {
		kind = "退出了房间"
	}
	log.Printf("房间 %s：%s %s", m.Room, dispName(j.label, j.seat), kind)
}

// forwardJoinCands 把加入方的**新**候选转给房主（打洞重协商用）。
//
// 背景：加入方的候选原来只在注册那一刻发一次。打洞失败后两边各自换端口重来，
// 房主不重新拿到这些地址，就只能对着**旧端口**发探测包 ⇒ 白打 ✗
//
// 🩸 候选是客户端给的、服务器不校验格式（只卡条数和长度，见 validateCands）——
// 转发前必须校验：不校验的话，对面会被几百条候选喂成发包机 ✗
func forwardJoinCands(r *room, j *member, cands []string) {
	if msg := validateCands(cands); msg != "" {
		log.Printf("房间 %s：%s 重发的候选不合法（%s），丢弃",
			r.code, dispName(j.label, j.seat), msg)
		return
	}
	r.mu.Lock()
	host := r.host
	r.mu.Unlock()
	if host == nil {
		return
	}
	// 带上 TargetID/Seat：房主靠它认领"这是哪一位的新地址" ✓
	//
	// 🩸🩸 字段必须叫 **Peer**，不是 Cands（2026-10-18 修）。
	//
	// 协议里这两个字段的分工是「谁的地址」：
	//
	//	Cands = **发这条消息的人自己**的候选
	//	Peer  = **对端**的候选
	//
	// 朋友发上来时用的是 Cands（自己的），但**转发给房主**时，从房主的角度看
	// 那就是"对端"的地址 ⇒ 必须是 Peer ✗ 原来这儿错填成 Cands，而房主读的是
	// m.Peer ⇒ 房主收到的永远是**空列表** ⇒ 重协商第二轮直接"没有可用的候选地址"。
	//
	// 后果：这个功能从加进来那天（4e2180c）起就没生效过，而且**不报错** ——
	// 现象只是"第二轮打洞必失败"，看着像网络问题 ✗
	if err := host.cl.Send(proto.Msg{
		Type: proto.TypeReCand, TargetID: j.id, Label: j.label, Seat: j.seat, Peer: cands,
	}); err != nil {
		log.Printf("房间 %s：把 %s 的新候选转给房主失败: %v",
			r.code, dispName(j.label, j.seat), err)
		return
	}
	log.Printf("房间 %s：已把 %s 的新候选转给房主", r.code, dispName(j.label, j.seat))
}
