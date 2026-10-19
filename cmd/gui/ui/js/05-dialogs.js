/* ---------- 通用提示弹窗 ---------- */
const alertModal = $('alertModal');

function showAlert(title, text) {
  if (!alertModal) return;
  $('alertTitle').textContent = title || '提示';
  $('alertText').textContent = text || '';
  alertModal.classList.add('show');
  $('alertOk').focus();
}
function closeAlert() { if (alertModal) alertModal.classList.remove('show'); }

// 标题按"当时在哪个页面"来定 —— 「没能加入房间」和「连接断开了」是两回事，
// 用户看到的字要对得上他刚做的事
function failTitle() {
  if (curPage === 'join') return '没能加入房间';
  if (curPage === 'create') return '没能创建房间';
  return '连接断开了';
}

if (alertModal) {
  $('alertOk').onclick = closeAlert;
  alertModal.onclick = e => { if (e.target === alertModal) closeAlert(); };
}

/* ---------- 关窗口弹窗 ---------- */
// 点 X 时后端会拦住关闭、用 Eval 调 showCloseDialog()。
// 如果"本次开机"已经记住过选择，后端就直接照做、根本走不到这儿。
function showCloseDialog() {
  const m = $('closeModal');
  if (!m) return;
  $('closeRemember').checked = false; // 每次弹都从"没勾"开始 —— 别替用户默认勾上
  m.classList.add('show');
}

(function bindCloseModal() {
  const m = $('closeModal');
  if (!m) return;
  const close = () => m.classList.remove('show'); // 只关掉，不执行任何动作
  const pick = action => {
    close();
    window.CloseChoice(action, $('closeRemember').checked);
  };
  $('closeTray').onclick = () => pick('tray');
  $('closeQuit').onclick = () => pick('quit');
  $('closeX').onclick = close;
  // 点弹窗外的空白处也关掉（跟其它弹窗一个行为）
  m.onclick = e => { if (e.target === m) close(); };
})();

// 托盘菜单用：窗口后端已经调出来了，这儿只管切到对应页。
// 「联机」回的是"联机流程里最后待的那页"，跟点侧边栏那个「联机」一致。
function gotoLink() { show(lastLinkPage); }
function gotoSettings() { show('settings'); }

window.onEvent = (name, data) => {
  if (name === 'state') {
    setStatus(data.text, data.level === 'ok' ? 'ok' : data.level === 'err' ? 'err' : '');
  }
  else if (name === 'log') addLog(data);
  else if (name === 'room') {
    // 房主：房间 ID 到手，进房间页（房间 ID 在那边显示，创建页不再摆一块）
    curMaxSize = parseInt(data.max) || 4;
    $('roomID').textContent = data.code;
    applyRoomPass(data.pass);
    if (curPage !== 'room') enterRoom(true, data.code, '');
  }
  else if (name === 'roompass') applyRoomPass(data);
  else if (name === 'members') renderMembers(data);
  else if (name === 'server') setServerState(data);
  else if (name === 'local') {
    // 服务器**收下**之后才会来这条（密码不对根本走不到这儿），带着地址进房间页。
    // 加入页上不再单摆一块地址 —— 地址本来就只有进房之后才有意义，
    // 房间页上有一行专门显示它。
    enterRoom(false, curJoinCode, data.addr);
  }
  else if (name === 'notice') {
    $('noticeBar').textContent = data;
    $('noticeBar').style.display = 'block';
    // 被踢 / 被拉黑 / 房主断了的时候人已经在房间页上了，join 页那根提示条根本看不见，
    // 所以顺手也推一份到状态栏
    setStatus(data, 'err');
  }
  else if (name === 'fail') {
    // 失败原因必须弹出来让人看见 —— 只写进「运行日志」等于没提示。
    // 密码不对、房间不存在、被踢、被拉黑、房主断了，走的都是这一条。
    // 此时**不切页**：在加入页就留在加入页，改完密码能直接重试。
    showAlert(failTitle(), data);
  }
  else if (name === 'live') {
    // 实时链路状况：挂在「房间信息」页，分三行列出（老板 2026-10-18 要求）✓
    setLiveInfo(data);
  }
  else if (name === 'punchfail') {
    // 🩸 打洞没打通**不是**房间结束（老板 2026-10-18）：
    //    原来这里走 fail 弹窗 + 退房间，日志跟着没了 —— 而那正是最需要日志的时候 ✗
    //    现在留在房间页，顶部挂一条横幅，可以直接导出日志，也可以重连 ✓
    showRoomFail(data);
  }
  else if (name === 'reconnecting') {
    // 断线自动重连：后端自己在试，界面只需要如实说"第几次、等多久" ✓
    showRoomRetry(data);
  }
  else if (name === 'reconnected') {
    // 重连回来了 ⇒ 横幅变绿，两秒后收掉（不用用户点任何东西）
    showRoomRetryOk();
  }
  else if (name === 'reconnectfail') {
    // 自动重连也没辙了 ⇒ 换成"要你动手"那条横幅（导出日志 / 手动重连）
    hideRoomRetry();
    showRoomFail(data, '重连没成功');
  }
  else if (name === 'stopped') {
    inRoom = false;
    $('btnCreate').disabled = false; $('btnJoin').disabled = false;
    $('btnStopCreate').disabled = true; $('btnStopJoin').disabled = true;
    ['port', 'maxSize', 'roomPass', 'codeInput', 'roomPassJoin'].forEach(id => {
      const el = $(id); if (el) el.disabled = false;
    });
    setStatus('未开始', '');
    // 🩸 成员列表是**服务器推**过来的，退出时不会自己清 —— 不清的话上一局的人会留在
    // DOM 里，下次进新房间（服务器名单到达前）就会露出来，看着像"房主还是上一个房主"。
    // 地址那类前端当场算的值不用管，它们进房间时会被覆盖。
    renderMembers([]);
    // 游戏那盏灯同理：清成"未知"，别让下一局一进来就顶着上一局的绿点/红点
    clearServerState();
    // 重连横幅也收掉 —— 半路点了「停止重连」时会走到这儿，不收就留在页上了 ✗
    hideRoomRetry();
    // 房间结束了，退回上一层重选
    if (curPage === 'room') show('mode');
  }
};

/* ---------- 个人信息页：换一个名字 ---------- */
// 名字只能从词库里摇（见 03-me.js 的 NICK_WORDS）—— 没有输入框，
// 所以这儿是"摇一个 + 立刻存"，跟头像「点一下即换」一个路子。
async function rollNewNick() {
  // 连点的时候别摇出跟当前一模一样那个：名字短的时候有概率撞上，
  // 看着像"按钮没反应"。
  let next = myNick;
  for (let i = 0; i < 8 && next === myNick; i++) next = rollNick();
  const err = await window.SetSetting('nick', next);
  if (err) { setStatus(err, 'err'); return; }
  applyNick(next);
  setStatus('新名字：' + next, 'ok');
}

if ($('btnRollNick')) {
  $('btnRollNick').onclick = rollNewNick;
}

// 建房 / 加房页那个「修改」→ 跳到个人信息页
document.querySelectorAll('[data-goto]').forEach(b => {
  b.onclick = () => show(b.dataset.goto);
});


/* ---------- 「没连上房主」横幅 ----------
 *
 * 三个动作：导出日志 / 重新连接 / （退出房间还是用房间页原来那个按钮）
 * 📌 重连**不清日志**：后端那边 RetryJoin 不会 reset ✓
 *
 * title 可传：打洞没打通是「没连上房主」，自动重连也失败是「重连没成功」——
 * 两种情况用户要做的事不一样，标题得说准 ✓
 */
function showRoomFail(msg, title) {
  const bar = $('roomFailBar');
  if (!bar) return;
  const t = $('roomFailTitle');
  if (t) t.textContent = title || '没连上房主';
  // 📌 横幅只放**第一句**：完整的三步引导在运行日志里已经有了，
  //    全塞进横幅会把房间内容挤下去（试过，四行高，很难看）✗
  const lines = String(msg || '').split('\n');
  // 第一句末尾的「按顺序试：」是给下面三步做引子的，单独挂在这儿很别扭 ✓
  const head = lines[0].trim().replace(/按顺序试：$/, '').replace(/。$/, '') + '。';
  const more = lines.length > 1 ? '（详细步骤在「运行日志」里）' : '';
  $('roomFailMsg').textContent = head + more;
  bar.hidden = false;

  const exp = $('btnExportOnFail');
  if (exp) {
    exp.onclick = () => {
      // 复用房间页那个导出入口，省得两套逻辑（它已经处理了勾选地址/格式）✓
      const b = document.querySelector('[data-export-log]');
      if (b) b.click();
    };
  }

  const retry = $('btnRetryJoin');
  if (retry) {
    retry.onclick = async () => {
      retry.disabled = true;
      retry.textContent = '重连中…';
      bar.hidden = true;
      // 日志里留一条分隔线：重连后能看清"这是第几次尝试" ✓
      addLog('—— 重新连接 ——');
      const err = await window.RetryJoin();
      retry.disabled = false;
      retry.textContent = '重新连接';
      if (err) {
        // 立刻失败（比如房间没了）就把横幅再挂回来，别让人干等
        showRoomFail(err);
      }
    };
  }
}

/* ---------- 「正在重连」横幅 ----------
 *
 * 老板 2026-10-18：网络抖一下、或者穿透服务器重启，原来整个房间就没了 ✗
 * 现在后端会自己退避重连（2/4/8/16/20 秒，最多 5 次），这儿只负责如实汇报进度 ✓
 */
let retryHideTimer = 0;

function hideRoomRetry() {
  const bar = $('roomRetryBar');
  if (!bar) return;
  clearTimeout(retryHideTimer);
  bar.hidden = true;
  bar.classList.remove('ok');
}

function showRoomRetry(d) {
  const bar = $('roomRetryBar');
  if (!bar) return;
  clearTimeout(retryHideTimer);
  bar.hidden = false;
  bar.classList.remove('ok');
  // 提示条上可能正挂着「和穿透服务器的连接断了。」—— 横幅说的更准（还带进度），
  // 两处同时说同一件事反而乱 ✓
  const nb = $('noticeBar');
  if (nb) nb.style.display = 'none';
  const n = Number(d && d.attempt) || 1;
  const max = Number(d && d.max) || 5;
  const reason = (d && d.reason) || '连接断了';
  $('roomRetryMsg').textContent =
    reason + '，正在自动重连（第 ' + n + '/' + max + ' 次）…';
}

function showRoomRetryOk() {
  const bar = $('roomRetryBar');
  if (!bar || bar.hidden) return; // 本来就没在重连 ⇒ 不弹这条
  bar.classList.add('ok');
  $('roomRetryMsg').textContent = '已经重新连上了';
  clearTimeout(retryHideTimer);
  // 两秒够看清，又不会一直占着地方 ✓
  retryHideTimer = setTimeout(hideRoomRetry, 2000);
}

(function bindCancelRetry() {
  const b = $('btnCancelRetry');
  if (!b) return;
  b.onclick = () => {
    // 走 LeaveRoom：它会把后端的重连循环打断（不然还要干等退避那 20 秒）✓
    b.disabled = true;
    window.LeaveRoom();
    setTimeout(() => { b.disabled = false; }, 1200);
  };
})();

/* ---------- 连接状况（房间信息页的三行） ----------
 *
 * 老板 2026-10-18：原来挤在左上角小卡片里只有一句，看不全 ✗
 * 现在拆成「连接方式 / 延迟 / 丢包」三行，放在房间信息页 ✓
 *
 * 颜色阈值（口语化，别搞太细）：
 *   延迟 ≤60 绿、≤150 黄、再高红
 *   丢包 0 绿、<5% 黄、再高红
 */
function setLiveInfo(d) {
  const rows = ['infoLiveRow', 'infoRttRow', 'infoLossRow'].map($);
  if (rows.some((r) => !r)) return;

  if (!d || !d.ok) {
    rows.forEach((r) => { r.hidden = true; });
    return;
  }
  rows.forEach((r) => { r.hidden = false; });

  // 连接方式：我们只做直连（没有中转），协议按对端地址族说 ✓
  $('infoLiveWay').textContent = '直连 · ' + (d.family === 'v6' ? 'IPv6' : 'IPv4');
  $('infoLiveWay').className = 'iv ok';

  const rtt = Number(d.rtt) || 0;
  $('infoRtt').textContent = rtt + ' ms';
  $('infoRtt').className = 'iv ' + (rtt <= 60 ? 'ok' : rtt <= 150 ? 'warn' : 'err');

  const loss = Number(d.loss) || 0;
  $('infoLoss').textContent = loss + '%';
  $('infoLoss').className = 'iv ' + (loss === 0 ? 'ok' : loss < 5 ? 'warn' : 'err');
}
