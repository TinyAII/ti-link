/* ---------- 房间页 ---------- */
// 创建 / 加入成功后进这里。房主和玩家共用这一页，只是按钮文案不同。

function switchRoomTab(name) {
  document.querySelectorAll('.rtab').forEach(t => t.classList.toggle('active', t.dataset.rt === name));
  document.querySelectorAll('.roompane').forEach(p => p.classList.toggle('active', p.id === 'pane-' + name));
}
document.querySelectorAll('.rtab').forEach(t => { t.onclick = () => switchRoomTab(t.dataset.rt); });

// 当前房间的密码（只有房主这边有值；朋友那边填空）
let curRoomPass = '';

// 把房间密码刷到卡片上。没设就写「未设置」并把复制按钮禁掉 ——
// 摆一个能点却复制不出东西的按钮，比不摆还烦人。
function applyRoomPass(p) {
  curRoomPass = p || '';
  // 两处显示同一个密码：左边房间卡那行、和「房间信息」页那行。
  // 改密码要同时刷，不然一页显示新的一页显示旧的。
  const el = $('roomPassShow');
  if (el) el.textContent = curRoomPass || '未设置';
  const iel = $('infoRoomPass');
  if (iel) iel.textContent = curRoomPass || '未设置';
  const c = $('copyRoomPass');
  if (c) c.disabled = !curRoomPass;
  const ic = $('copyInfoRoomPass');
  if (ic) ic.disabled = !curRoomPass;
}

// 进房间。id / addr 传空就不覆盖（房主的房间ID 是后端稍后推过来的）
function enterRoom(isHost, id, addr) {
  roomIsHost = isHost;
  inRoom = true;
  // 进房间先把「没连上房主」那条横幅收掉 —— 上一局留下的东西不该带到新房间 ✓
  const failBar = $('roomFailBar');
  if (failBar) failBar.hidden = true;
  // 连接状况也是上一局的了 —— 先收起来，等后端推新的 ✓
  ['infoLiveRow', 'infoRttRow', 'infoLossRow'].forEach((id) => {
    const el = $(id);
    if (el) el.hidden = true;
  });
  // 房间 ID / 连接地址要同时刷到两处：左边房间卡，和「房间信息」页。
  // 别再让它们各显示各的 —— 那是最容易出"一页新一页旧"的地方。
  if (id) { $('roomID').textContent = id; $('infoRoomID').textContent = id; }
  // 🩸 显示给用户的地址要**按游戏**来：星露谷只填 IP、不带端口（端口是内置的）——
  // 照搬 "主机:端口" 那串，粘进游戏里是不认的 ✗
  // ⚠️ 只转显示这一层：内部（隧道监听、那个状态灯的探测）用的还是带端口那份 ✓
  if (addr) {
    const shown = addrForGame(addr, curGameName);
    $('roomAddr').textContent = shown;
    $('infoRoomAddr').textContent = shown;
  }
  $('btnRoomMain').textContent = isHost ? '关闭房间' : '退出房间';
  $('roomMenu').hidden = true;
  // 「修改密码」和「管理黑名单」都是房主才有的操作，朋友那边连 ≡ 都不该看见
  $('btnRoomMenu').hidden = !isHost;
  $('passField').hidden = !isHost;
  // 房间密码那一行只有房主看得到（朋友自己刚填过，再摆一行纯属噪音）
  $('infoPassRow').hidden = !isHost;
  // 玩家侧那行同理（原来整行都藏起来，现在改成"换一句话说"）
  $('infoServerRow').hidden = false;

  // 🩸 身份刚定下来 ⇒ 把那一格**按新身份重刷一遍**。
  // 后端可能**比这儿更早**就把探测结果推过来了（CreateRoom 一返回它就开始探），
  // 那时 roomIsHost 还是上一局的值 ⇒ 房主会看到玩家的那句话 ✗
  // （2026-10-15 老板碰到的："还没连上房主服务器"出现在房主界面上）
  renderServerState();
  $('infoServerLabel').textContent = isHost ? '游戏服务端' : '游戏连接';
  // 🩸 这里**不能**把灯清成"未知"：房主那边的轮询只在「启动时」和「状态变化时」推，
  // 进房间之后再清，就永远等不到下一次推送 —— 灯会一直停在「—」✗（犯过一次）
  // 清空放在**会话结束**时做，见 clearServerState / stopped 分支。
  // 默认停在「房间信息」—— 它是第一个标签，也是进房间后最想先看的那页
  switchRoomTab('info');
  show('room');
}

// 二次确认（关房 / 退房都要过这一关）
function askConfirm(title, text, okText, onOk) {
  const m = $('confirmModal');
  $('confirmTitle').textContent = title;
  $('confirmText').textContent = text;
  $('confirmYes').textContent = okText || '确认';
  m.classList.add('show');
  const close = () => m.classList.remove('show');
  $('confirmNo').onclick = close;
  m.onclick = e => { if (e.target === m) close(); };
  $('confirmYes').onclick = () => { close(); onOk(); };
}

if ($('btnRoomMain')) {
  $('btnRoomMain').onclick = () => {
    if (roomIsHost) {
      askConfirm('关闭房间', '房间会被关掉，里面所有人都会断开连接。确定吗？', '关闭房间',
        () => window.CloseRoom());
    } else {
      askConfirm('退出房间', '退出后你与房主的连接会断开。确定吗？', '退出房间',
        () => window.LeaveRoom());
    }
  };
}

// 三条杠：修改房间密码 / 管理黑名单（都是房主的功能）
if ($('btnRoomMenu')) {
  $('btnRoomMenu').onclick = e => {
    e.stopPropagation();
    $('roomMenu').hidden = !$('roomMenu').hidden;
  };
  document.addEventListener('click', () => { if ($('roomMenu')) $('roomMenu').hidden = true; });
  $('roomMenu').onclick = e => {
    const act = e.target.dataset ? e.target.dataset.roomAct : '';
    if (!act) return;
    $('roomMenu').hidden = true;
    if (act === 'pass') openPassModal();
    else if (act === 'ban') openBanModal();
  };
}

if ($('copyRoomID')) $('copyRoomID').onclick = () => copyText($('roomID').textContent, '房间 ID 已复制');
if ($('copyRoomAddr')) $('copyRoomAddr').onclick = () => copyText($('roomAddr').textContent, '地址已复制，粘到游戏里即可');
if ($('copyRoomPass')) {
  $('copyRoomPass').onclick = () => { if (curRoomPass) copyText(curRoomPass, '房间密码已复制'); };
}
// 「房间信息」页那三个复制按钮 —— 跟左边房间卡上的功能一样，
// 但新用户更容易看到这一页，所以两处都给。
if ($('copyInfoRoomID')) $('copyInfoRoomID').onclick = () => copyText($('infoRoomID').textContent, '房间 ID 已复制');
if ($('copyInfoRoomAddr')) $('copyInfoRoomAddr').onclick = () => copyText($('infoRoomAddr').textContent, '地址已复制，粘到游戏里即可');
if ($('copyInfoRoomPass')) {
  $('copyInfoRoomPass').onclick = () => { if (curRoomPass) copyText(curRoomPass, '房间密码已复制'); };
}

/* ---------- 修改房间密码（房主） ---------- */
const passModal = $('passModal');

function openPassModal() {
  if (!passModal) return;
  // 把当前密码带出来：想改一个字的话，比全删了重打省事
  $('passInput').value = curRoomPass;
  $('passHint').textContent = '要设就至少 4 位；留空 = 不设密码。';
  $('passOk').disabled = false;
  passModal.classList.add('show');
  $('passInput').focus();
}
function closePassModal() { if (passModal) passModal.classList.remove('show'); }

if (passModal) {
  $('passCancel').onclick = closePassModal;
  passModal.onclick = e => { if (e.target === passModal) closePassModal(); };
  $('passOk').onclick = async () => {
    const v = $('passInput').value.trim();
    $('passOk').disabled = true;
    const err = await window.SetRoomPass(v);
    $('passOk').disabled = false;
    // 校验没过就不关窗 —— 让人看着提示改，别把窗一关就不知道错哪了
    if (err) { $('passHint').textContent = err; return; }
    closePassModal();
    applyRoomPass(v);
    setStatus(v ? '房间密码已更新' : '已取消房间密码', 'ok');
    addLog(v
      ? '房间密码已更新 —— 之后加入的朋友要用新密码（已在房里的不受影响）'
      : '已取消房间密码，之后任何人都能加入');
  };
}

/* ---------- 管理黑名单（房主） ---------- */
const banModal = $('banModal');

function renderBanList(list) {
  const el = $('banList');
  if (!el) return;
  el.innerHTML = '';
  if (!list.length) {
    const d = document.createElement('div');
    d.className = 'banrow empty';
    d.textContent = '黑名单是空的。在成员列表里点「拉黑」，人就会出现在这儿。';
    el.appendChild(d);
    return;
  }
  list.forEach(b => {
    const row = document.createElement('div');
    row.className = 'banrow';

    const who = document.createElement('span');
    who.className = 'who';
    who.textContent = b.label || '（没留下名字）';
    row.appendChild(who);

    // 设备 ID 前几位：两个人都叫「小明」的时候，靠它分清哪个是哪个
    if (b.short) {
      const s = document.createElement('span');
      s.className = 'sub mono';
      s.textContent = b.short;
      row.appendChild(s);
    }

    const btn = document.createElement('button');
    btn.className = 'op';
    btn.textContent = '解除';
    btn.onclick = async () => {
      btn.disabled = true;
      const err = await window.Unban(b.key);
      if (err) { setStatus(err, 'err'); btn.disabled = false; return; }
      addLog('已把「' + (b.label || '（没留下名字）') + '」移出黑名单');
      setStatus('已解除拉黑', 'ok');
      openBanModal(); // 重新拉一份列表，别自己在前端删行假装成功
    };
    row.appendChild(btn);
    el.appendChild(row);
  });
}

async function openBanModal() {
  if (!banModal) return;
  let list = [];
  try { list = (await window.GetBanList()) || []; } catch (e) { list = []; }
  renderBanList(list);
  banModal.classList.add('show');
}
function closeBanModal() { if (banModal) banModal.classList.remove('show'); }

if (banModal) {
  $('banClose').onclick = closeBanModal;
  banModal.onclick = e => { if (e.target === banModal) closeBanModal(); };
}

// Esc 关掉这几个浮层（导出那个弹窗自己有处理）
document.addEventListener('keydown', e => {
  if (e.key !== 'Escape') return;
  closePassModal();
  closeBanModal();
  closeMemMenu();
  closeAlert();
});

/* ---------- 导出日志：单独的小窗口 ---------- */
const exportModal = $('exportModal');
if (exportModal) {
  const openExport = () => {
    // 每次都从"不勾"开始 —— 地址这东西不该默认带上
    $('exportAddr').checked = false;
    exportModal.classList.add('show');
  };
  const closeExport = () => exportModal.classList.remove('show');

  // 所有「导出」入口都打开同一个小窗口。
  //
  // 🩸 原来这里是按 id 拼的：`$('exportLog' + s)`，s 取 Create / Join。
  //    后来「独立房间页」那次改版把按钮改成了 `exportLogRoom`，**JS 没跟着改**
  //    ⇒ 找的是两个根本不存在的 id（`if (b)` 又把 null 挡掉了）
  //    ⇒ **点「导出」完全没反应**，而且不报错，看起来就像"导出失败" ✗
  //    （2026-10-18 用户报的，实际从独立房间页那天起就没工作过）
  //
  // 📌 改成按 **data 属性**找：以后在哪儿加导出入口都自动生效，
  //    再也不会出现"改了一边忘另一边" ✓
  document.querySelectorAll('[data-export-log]').forEach(b => {
    b.onclick = openExport;
  });

  $('exportCancel').onclick = closeExport;
  // 点遮罩、按 Esc 也能关掉
  exportModal.onclick = e => { if (e.target === exportModal) closeExport(); };
  document.addEventListener('keydown', e => {
    if (e.key === 'Escape') closeExport();
  });

  $('exportOk').onclick = async () => {
    if (!window.ExportLog) return;
    const picked = document.querySelector('input[name="logfmt"]:checked');
    // 📌 导出会**顺便跑一遍四项自检**（连 STUN，最坏几秒）——
    //    按钮上得有个"在干活"的信号，否则用户以为点了没反应 ✗
    $('exportOk').disabled = true;
    $('exportOk').textContent = '收集中…';
    const msg = await window.ExportLog($('exportAddr').checked, picked ? picked.value : 'md');
    $('exportOk').disabled = false;
    $('exportOk').textContent = '导出';
    closeExport();
    if (msg) setStatus(msg, 'ok');
  };
}

/* ---------- 按钮 ---------- */
// 开新会话前把日志清干净。
// 注意是"进房前清"而不是"退出时清" —— 被踢 / 出错断开的那种，
// 日志正是要来排查的，不能因为退出就抹掉。
function clearRoomLog() {
  const t = $('termRoom');
  if (t) t.innerHTML = '';
}

// 🩸 游戏是**必填**的（2026-10-16 晚）：不选就不让建房/加入。
// 服务器那边也会拒 —— 这儿只是**把话说在前面**，别让用户点完等一圈才被拒 ✓
function gameChosenOrComplain() {
  if (curGameName) return true;
  setStatus('先在上面选一款游戏（我的世界 / 泰拉瑞亚 / …），再建房或加入。', 'err');
  return false;
}

$('btnCreate').onclick = async () => {
  if (!gameChosenOrComplain()) return;
  $('btnCreate').disabled = true;
  clearRoomLog();
  curMaxSize = parseInt($('maxSize').value) || 4;
  // 第二个参数是"第二路端口"（饥荒洞穴那类）。没有就传空串 ⇒ 不开第二路 ✓
  // 📌 从**输入框**读，不是从 GAMES 硬读 —— 用户改了主端口时这里是能跟着改的 ✓
  const extraPort = $('extraPort') ? $('extraPort').value.trim() : '';
  const err = await window.CreateRoom(
    $('port').value, extraPort, $('maxSize').value, myNick, $('roomPass').value, myAvatarIdx,
    gameProto(), probeModesOf(curGameName), curGameName);
  if (err) { setStatus(err, 'err'); $('btnCreate').disabled = false; return; }
  $('btnStopCreate').disabled = false;

  // 记下"**这个游戏**用过的端口"（开着「记住每个游戏用过的端口」时才真记）——
  // 🩸 必须由页面来记：Go 那边看不到当前选的是哪个游戏（curGameName 是前端状态）✓
  if (window.RememberGamePort && curGameName) {
    window.RememberGamePort(curGameName, $('port').value);
  }
  $('port').disabled = true; $('maxSize').disabled = true;
  $('roomPass').disabled = true;
  // 房主这边看到的"连接地址"就是自己本机服务端的地址（确认填对没有）
  enterRoom(true, '', '127.0.0.1:' + ($('port').value || '25565'));
  // 用户填的密码先摆上，等后端回调再以它为准（两边应当一致）
  applyRoomPass($('roomPass').value.trim());
};

// 房间 ID 里的字母统一大写：用户敲小写就**当场**给他换掉，
// 而不是等他点"加入"才悄悄变样。房间 ID 里只有前缀和后缀是字母，
// 中间是数字，所以整串 toUpperCase 是安全的。
if ($('codeInput')) {
  $('codeInput').addEventListener('input', () => {
    const el = $('codeInput');
    const up = el.value.toUpperCase();
    if (up === el.value) return;
    // 换完把光标放回原处，别让它跳到末尾（长度没变，位置仍然有效）
    const s = el.selectionStart, e = el.selectionEnd;
    el.value = up;
    try { el.setSelectionRange(s, e); } catch (_) {}
  });
}

$('btnJoin').onclick = async () => {
  if (!gameChosenOrComplain()) return;
  $('btnJoin').disabled = true;
  clearRoomLog();
  curJoinCode = $('codeInput').value.trim().toUpperCase().replace(/\s+/g, '');
  // 最后一个参数是游戏名：顺手报给服务器（后台看板显示/筛选用）✓
  // 📌 它是自报字段 —— 别拿它当"校验对方"的依据 ✗
  // 第五个参数之后是"第二路端口"：饥荒这类"一个游戏两个端口"的用它 ✓
  // （房主没开第二路的话，node 那边也不会真去监听它 ✓）
  const jg = curGame();
  const extraHint = (jg && jg.extraPort) ? String(jg.extraPort) : '';
  const err = await window.JoinRoom($('codeInput').value, myNick, $('roomPassJoin').value,
    myAvatarIdx, gamePortHint(), extraHint, gameProto(), curGameName);
  if (err) { setStatus(err, 'err'); $('btnJoin').disabled = false; return; }
  $('btnStopJoin').disabled = false;
  $('codeInput').disabled = true; $('roomPassJoin').disabled = true;
};

$('btnStopCreate').onclick = () => window.Stop();
$('btnStopJoin').onclick = () => window.Stop();

