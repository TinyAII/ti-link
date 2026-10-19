/* ---------- 状态 ---------- */
function setStatus(text, level) {
  // 状态落在两个地方：
  //   ① 房间页标签栏右侧那个 #roomState
  //   ② 出错 / 提示走弹窗（见 showAlert）
  // 建房 / 加房页**不再显示状态**（老板要求页面只留"创建 / 加入房间"本身）；
  // 底部横条也已经去掉。所以这儿只剩房间页那一处要写 —— 别再往回加。
  const rs = $('roomState');
  if (rs) rs.textContent = text;
}

// 日志只有一处 —— 房间页
function termOf() { return $('termRoom'); }

function addLog(msg) {
  const t = termOf();
  const ph = t.querySelector('.ph');
  if (ph) ph.remove();
  const div = document.createElement('div');
  div.className = 'ln';
  div.textContent = msg;
  t.appendChild(div);
  t.scrollTop = t.scrollHeight;
}

/* ---------- 成员行的小菜单（踢出 / 拉黑） ---------- */
const memMenu = $('memMenu');
let memMenuTarget = null;

// 在 (x, y) 处打开菜单，m 是这位成员的 {id,label,seat,host}
function openMemMenu(x, y, m) {
  if (!memMenu) return;
  memMenuTarget = m;
  memMenu.hidden = false;
  // 先显示出来再量尺寸 —— 贴着窗口边时得往回收，别让菜单被切掉
  const r = memMenu.getBoundingClientRect();
  memMenu.style.left = Math.max(8, Math.min(x, window.innerWidth - r.width - 8)) + 'px';
  memMenu.style.top = Math.max(8, Math.min(y, window.innerHeight - r.height - 8)) + 'px';
}

function closeMemMenu() {
  if (memMenu) memMenu.hidden = true;
  memMenuTarget = null;
}

if (memMenu) {
  memMenu.onclick = e => {
    const act = e.target.dataset ? e.target.dataset.memAct : '';
    if (!act || !memMenuTarget) return;
    const id = memMenuTarget.id;
    closeMemMenu();
    if (act === 'kick') window.Kick(id);
    else if (act === 'ban') window.Ban(id);
  };
  // 点别处就关。用捕获阶段：先判断该不该关，再轮到行自己的 onclick
  document.addEventListener('click', e => {
    if (!memMenu.hidden && !memMenu.contains(e.target)) closeMemMenu();
  }, true);
  // 滚一下就关 —— 它按视口坐标 fixed 定位，不关会飘在原地
  document.addEventListener('scroll', () => { if (!memMenu.hidden) closeMemMenu(); }, true);
}

/* ---------- 成员列表 ---------- */

// 把服务器的「纯昵称 + 座位号」拼成界面上要显示的两截。
// 规则跟 proto.DisplayName 一致（那边是给日志/黑名单用的纯文本版）。
function memberParts(m) {
  const host = !!m.host;
  let name = m.label || '';
  // 兼容还在跑老版服务器的情形：那版把兜底名整串塞进了 label（「玩家3」），
  // 再拼一次前缀就成了「玩家 玩家3」。只剥「角色+纯数字」，
  // 免得把真管自己叫「玩家粉」的人削掉一个字。
  const fb = /^(?:房主|玩家)(\d+)$/.exec(name);
  if (fb) name = host ? '' : fb[1];
  if (!name && !host) name = m.seat ? String(m.seat) : '';
  const role = host ? '房主' : '玩家';
  // 头像取哪个字：有真名字就取名字的首字；没起名时 name 只是个座位号（「1」），
  // 取它当头像没意义 —— 这时候用角色那截（「玩」）
  const avatarSrc = (name && !/^\d+$/.test(name)) ? name : role;
  return { role: role, name: name, host: host, avatarSrc: avatarSrc };
}

function renderMembers(list) {
  const el = $('memList2');
  if (!el) return;
  // 名单一变，正开着的小菜单就失效了 —— 它指的那个人可能已经走了
  closeMemMenu();
  // 人数上限只有房主知道（服务器只发给房主），朋友这边就只报人数，别编个分母出来
  $('memCount2').textContent = roomIsHost
    ? '(' + list.length + ' / ' + curMaxSize + ')'
    : '(' + list.length + ' 人)';
  el.innerHTML = '';
  if (!list.length) {
    const d = document.createElement('div');
    d.className = 'memrow empty';
    d.textContent = '还没有人';
    el.appendChild(d);
    return;
  }
  list.forEach(m => {
    const row = document.createElement('div');
    row.className = 'memrow';

    // 角色前缀 + 名字：房主是「房主 小明」，没起名就是「房主」/「玩家 1」
    const p = memberParts(m);

    const av = document.createElement('span');
    av.className = 'avatar';
    row.appendChild(av);
    applyAvatar(av, m.avatar, p.avatarSrc, p.role);

    const role = document.createElement('span');
    role.className = 'role' + (p.host ? ' owner' : '');
    role.textContent = p.role;
    row.appendChild(role);

    if (p.name) {
      const who = document.createElement('span');
      who.className = 'who';
      who.textContent = p.name;
      row.appendChild(who);
    }

    // 他报的游戏。
    // 🩸 两条刻意的：① **没报就不显示** —— 老客户端一大堆"未上报"只是噪音 ✗
    //              ② 跟我选的**不一样**才加警示色（服务端也会发一句文字提示，
    //                 这个是"扫一眼就看见"的那层）✓
    // ⚠️ 自报字段：只当线索，别拿它去质疑谁 ✗（见 proto.Msg.Game）
    if (m.game) {
      const gs = document.createElement('span');
      gs.className = 'memgame' + (curGameName && m.game !== curGameName ? ' diff' : '');
      gs.textContent = m.game;
      gs.title = gs.classList.contains('diff')
        ? '他报的游戏跟你选的不一样 —— 确认没进错房间？'
        : '他自己报的在玩的游戏';
      row.appendChild(gs);
    }

    // 踢人 / 拉黑是房主的操作，朋友那边整行一个按钮都没有
    if (!p.host && roomIsHost) {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'rowmenu';
      btn.textContent = '⋮';
      btn.title = '更多操作';
      btn.setAttribute('aria-label', '更多操作');
      btn.onclick = e => {
        e.stopPropagation();
        const r = btn.getBoundingClientRect();
        openMemMenu(r.left, r.bottom + 4, m);
      };
      row.appendChild(btn);
      // 右键整行也能叫出菜单
      row.oncontextmenu = e => { e.preventDefault(); openMemMenu(e.clientX, e.clientY, m); };
    }
    el.appendChild(row);
  });
}

// 游戏那边的状态灯。**两边看的不是同一件事**：
//   房主 —— 本机游戏服务端开了没（绿=已连上；红=连不上，呼吸闪，提醒去开）
//   玩家 —— 自己的游戏连上房间了没（绿=已连入；灰=还没）
//
// 🩸 为什么玩家不能用房主那套探测：房主探的是 127.0.0.1:<端口>（自己的服务端），
// 而玩家那边同一个端口是**自己的转发监听**，隧道都没建好它就已经在监听了 ⇒
// 探出来永远是绿的，房主游戏没开也显示「已连上」——那就成了假指示灯。
// 玩家这边换成真实信号：后端从 node 的 StateBridging（「游戏已连入，数据转发中」）推过来。
// 最后一次收到的状态。
//
// 形状有三种，**别混**：
//
//	{up:true,  stage:'bridging'}  隧道通了、游戏也连进来了
//	{up:true,  stage:'tunnel'}    隧道通了，游戏还没连 —— **已经能玩了**
//	{up:false, stage:'punching'}  还在连（正常的中间态）
//	{up:false, stage:'failed'}    真失败了
//	{up:null}                     测不了（房主侧的游戏不支持探测）
//	null                          从来没收到 / 已清空
let lastServer = null;

function setServerState(data) {
  lastServer = data || null;
  renderServerState();
}

// 按**当前**身份把那一格重新渲染一遍。
// 身份变了（进房间）时也要调一次 —— 见 enterRoom 末尾 ✓
function renderServerState() {
  const dot = $('infoServerDot');
  const txt = $('infoServerText');
  if (!dot || !txt) return;
  const d = lastServer;
  const up = d ? d.up : null;
  const stage = (d && d.stage) || '';

  // 🩸 up === null ⇒ **这个游戏/此刻判断不了**。
  //
  // 判断不了有好几种可能：
  //   · probeMode 是 "none"
  //   · 查本机端口时权限不够（读不出进程名）
  //   · 非 Windows 平台
  // 不管是哪种，都**别照常显示"连不上"** ——
  // 那会让用户以为自己没开好、在那儿白折腾 ✗
  // （2026-10-15 老板星露谷明明开着，界面上报的就是这个）
  //
  // ⇒ 如实说"测不了"，宁可不给判断，也不给一个**错的**判断 ✓
  if (up === null || up === undefined) {
    dot.classList.remove('up', 'down');
    txt.classList.remove('down');
    txt.textContent = roomIsHost
      ? '这个游戏没法自动检测 —— 游戏里开好房间就行'
      : '这个游戏不作检测';
    return;
  }

  // 🩸 「正在连」和「没连上」的**颜色也必须分开**（2026-10-18）。
  //
  // 原来一律 `!up` 就加 down ⇒ 打洞中那二十几秒是**大红字 + 红点**，
  // 跟真失败长得一模一样 —— 新玩家当然以为软件出了故障 ✗
  //
  // 现在：绿 = 能玩；红 = 真失败（房主侧"服务端没开"、玩家侧"没连上"）；
  //       灰 = 正在连（正常过程，不该报警）✓
  const failed = !up && (roomIsHost || stage === 'failed');
  dot.classList.toggle('up', !!up);
  dot.classList.toggle('down', failed);
  // 🩸 这一格会随身份换整句话，长度差别很大 ⇒ 样式里给它开了换行、
  // 并且**不加省略号**（见 style.css 的 #infoServerState）。
  txt.textContent = roomIsHost
    ? (up ? '已连上' : '连不上 —— 先把游戏服务端开起来')
    : playerServerText(up, stage);
  txt.classList.toggle('down', failed);
}

// playerServerText 玩家侧那一格该写什么。
//
// 🩸 三档语义完全不同，**别合并**（老板 2026-10-18 提的）：
//
//	已连上（隧道通了）      ⇒ 能玩了
//	正在连接（打洞/等房主）  ⇒ **正常的中间态，不是故障**
//	没连上（真失败了）      ⇒ 才该报警
//
// 原来只有 up / !up 两档，于是**刚进房间、隧道还在打洞**的那二三十秒里，
// 那格写着「还没连上房主服务器」—— 第一次用的人会以为出问题了 ✗
// 实际上那正是正常的连接过程，该说的是「正在连接」而不是「没连上」✓
function playerServerText(up, stage) {
  if (up) {
    // 📌 隧道通了 / 游戏也连进来了 —— 对玩家来说都是"能玩"，
    //    差别只在于要不要现在就去开游戏 ✓
    return stage === 'bridging' ? '已连接房主服务器' : '已连上房主 —— 进游戏就能玩';
  }
  if (stage === 'failed') {
    return '没连上房主';
  }
  return '正在连接房主…';
}

// clearServerState 把灯清成"未知"，下一局重新开始。
//
// 🩸 **只能在会话结束时调**（stopped 分支），**绝不能放进 enterRoom** ✗ ——
// 房主那边的轮询只在「启动时」和「状态变化时」推一次，
// 进房间之后再清就永远等不到下一次推送，灯会一直停在「—」（这个 bug 犯过一次 ✓）。
// 跟旁边那句 renderMembers([]) 是一个道理：都是"别把上一局的东西留到下一局"。
function clearServerState() {
  const dot = $('infoServerDot');
  const txt = $('infoServerText');
  if (!dot || !txt) return;
  // 顺手把记的结果也清掉 —— 不然下一局进房间时那个重刷会拿上一局的旧值 ✗
  lastServer = null;
  dot.className = 'srvdot';
  txt.textContent = '—';
  txt.classList.remove('down');
}

