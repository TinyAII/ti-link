/* ---------- 账号：注册 / 登录 / 退出 ----------
 *
 * 为什么单独一个文件、而且编号最大（10）：
 * assets.go 是按**文件名顺序**把 ui/js/*.js 拼成一份脚本的，
 * 而这个文件必须在 08-window.js 之后执行 —— 08 里已经做了
 * 「按 hash 挑初始页」和 `__ready()`（告诉 Go 可以显示窗口了）。
 * 排在它后面，我们才有机会在**窗口显示出来之前**把页面换成登录页，
 * 不然会先闪一下主界面 ✗
 *
 * 🩸 判"登没登录"用的是**拼页面时就填好的属性**（body 的 data-logged-in），
 * 不是去问 Go —— 问 Go 是异步的，同样会闪。见 assets.go 里那段注释 ✓
 */

const AUTH = {
  loggedIn: document.body.dataset.loggedIn === '1',
  server: document.body.dataset.loginBase || '',
  email: ''
};

/* 一行提示：ok 传 true 显示绿色，null 表示"正在做…"（灰色） */
function authMsg(id, text, ok) {
  const el = $(id);
  if (!el) return;
  el.textContent = text || '';
  el.className = 'loginmsg' + (ok === true ? ' ok' : '');
}

function setGated(on) {
  document.body.classList.toggle('gated', on);
}

function applyAuthToProfile() {
  const el = $('accEmail');
  if (el) el.textContent = AUTH.email || '—';
}

/* 登录成功之后：把门放开、回到联机首页、把邮箱填进个人信息页 */
function onLoggedIn(email) {
  AUTH.loggedIn = true;
  AUTH.email = email || '';
  setGated(false);
  // 密码和邀请码用完就别留在框里了
  $('loginPass').value = '';
  $('suPass').value = '';
  $('suCode').value = '';
  authMsg('loginMsg', '');
  authMsg('suMsg', '');
  applyAuthToProfile();
  show('games');
}

/* ---------- 启动时的门禁 ---------- */

if (!AUTH.loggedIn) {
  setGated(true);
  show('login');
} else {
  // 本地有令牌 ⇒ 先按"登录着"放行，**不等网络**（等的话窗口要晚几百毫秒才出来）。
  // 令牌到底还有效没有，放到后台悄悄验 ✓
  applyAuthToProfile();
  verifyInBackground();
}

/* 后台验一下本地令牌：过期了就把人请回登录页，网络不通就当没事发生。
 *
 * 🩸 这两种情况必须分开（服务端那边也是这么分的）：
 *   过期  → 清掉本地令牌 + 回登录页（留着没用了）
 *   没网  → **什么都不做**！网断一下就把人登出，下次还得重输密码，纯属添乱 ✗ */
function verifyInBackground() {
  if (!window.VerifyAccount) return;
  window.VerifyAccount().then(r => {
    if (!r) return;
    if (r.expired) {
      AUTH.loggedIn = false;
      AUTH.email = '';
      setGated(true);
      show('login');
      authMsg('loginMsg', r.error || '登录已过期，请重新登录', false);
      return;
    }
    if (r.ok) {
      AUTH.email = r.email || '';
      applyAuthToProfile();
    }
    // r.ok === false 且不是 expired ⇒ 网络问题，保持现状
  }).catch(() => {});
}

/* ---------- 登录 / 注册两块的切换 ---------- */

function showPane(which) {
  $('pane-login').hidden = which !== 'login';
  $('pane-signup').hidden = which !== 'signup';
  if ($('pane-reset')) $('pane-reset').hidden = which !== 'reset';
  authMsg('loginMsg', '');
  authMsg('suMsg', '');
  authMsg('rsMsg', '');
  const first = which === 'login' ? $('loginEmail')
              : which === 'signup' ? $('suCode')
              : $('rsEmail');
  if (first) first.focus();
  if (which === 'signup') refreshSignupMode();
}

/* ---------- 注册页：邀请码是"必填"还是"可选" ----------
 *
 * 🩸 不能把「（可选）」写死在 HTML 里：管理员随时能改回"必须邀请码" ✗
 *    写死就变成骗人 —— 用户留空、提交、被拒，还说不出哪里不对 ✓
 * ⇒ 进这一页时问一次服务器，按实际模式写字 ✓
 *
 * 📌 问不到（网络错/后端没起来）按**要码**处理：
 *    宁可多要求一个码，也别让人以为能留空然后被拒 ✓ */
// 现在注册要不要邀请码。默认 true（保守：问不到就按"要"处理）
let signupNeedsCode = true;

async function refreshSignupMode() {
  const hint = $('suCodeHint');
  const sub = $('suSub');
  if (!hint || !window.SignupMode) return;

  let mode = '?';
  try {
    mode = await window.SignupMode();
  } catch (e) {
    mode = '?';
  }

  if (mode === '可选') {
    signupNeedsCode = false;
    hint.textContent = '（可选）';
    sub.textContent = '有邀请码就填，没有可以留空';
  } else {
    signupNeedsCode = true;
    // 必填是默认状态，不用特意标注 ✓
    hint.textContent = '';
    sub.textContent = '邀请码是私发的，不是谁都能注册';
  }
}

if ($('toSignup')) $('toSignup').onclick = e => { e.preventDefault(); showPane('signup'); };
if ($('toLogin')) $('toLogin').onclick = e => { e.preventDefault(); showPane('login'); };
if ($('toReset')) $('toReset').onclick = e => { e.preventDefault(); showPane('reset'); };
if ($('toLogin2')) $('toLogin2').onclick = e => { e.preventDefault(); showPane('login'); };

/* ---------- 找回密码（2026-10-17）---------- */
// 📌 发码复用 purpose='login'：条件一样（邮箱必须已注册），后端不用另开分支 ✓
if ($('btnSendRsCode')) {
  $('btnSendRsCode').onclick = () =>
    requestCode($('btnSendRsCode'), 'rsMsg', 'reset', $('rsEmail').value.trim(), '');
}

if ($('rsBtn')) {
  $('rsBtn').onclick = async () => {
    const email = $('rsEmail').value.trim();
    const code = $('rsCode').value.trim();
    const pass = $('rsPass').value;
    if (!email) { authMsg('rsMsg', '请先填邮箱', false); return; }
    if (!/^\d{6}$/.test(code)) { authMsg('rsMsg', '验证码是 6 位数字', false); return; }
    if (pass.length < 8) { authMsg('rsMsg', '新密码至少 8 位', false); return; }
    if (!window.ResetPassword) { authMsg('rsMsg', '界面和后端没接上，重启一下试试', false); return; }

    const btn = $('rsBtn');
    btn.disabled = true;
    authMsg('rsMsg', '正在改…', null);
    let err = '';
    try {
      err = await window.ResetPassword(email, code, pass);
    } catch (e) {
      err = '改密码失败：' + e;
    }
    btn.disabled = false;
    if (err) { authMsg('rsMsg', err, false); return; }

    // 成功：切回登录页，把邮箱替他填好（省得再打一遍）✓
    showPane('login');
    $('loginEmail').value = email;
    $('loginPass').value = '';
    $('loginPass').focus();
    authMsg('loginMsg', '密码改好了，用新密码登录 ✓');
  };
}

/* ---------- 验证码（登录和注册共用这一段）---------- */

// 验证码在服务器那边的有效期，跟 cmd/panel/mailcode.go 的 codeTTL 对齐。
// 📌 文案里说"5 分钟"就得真是 5 分钟 —— 说错了用户会以为没过期 ✗
const CODE_TTL_MIN = 5;

/* 按住的 60 秒重发倒计时。
 * 🩸 不光是"好看"：服务器那边真有 60 秒重发间隔，
 * 不灰掉按钮的话用户会连点、然后每次都被"刚发过了"挡回来，看着像坏了 ✗ */
function startCountdown(btn, secs) {
  const label = btn.textContent;
  let n = secs;
  btn.disabled = true;
  btn.textContent = n + ' 秒后重发';
  const t = setInterval(() => {
    n--;
    if (n <= 0) {
      clearInterval(t);
      btn.disabled = false;
      btn.textContent = label;
      return;
    }
    btn.textContent = n + ' 秒后重发';
  }, 1000);
}

async function requestCode(btn, msgId, purpose, email, invite) {
  if (!window.SendCode) { authMsg(msgId, '界面和后端没接上，重启一下试试', false); return; }
  if (!email) { authMsg(msgId, '请先填邮箱', false); return; }
  btn.disabled = true;
  authMsg(msgId, '正在发送…', null);
  let err = '';
  try {
    err = await window.SendCode(email, purpose, invite || '');
  } catch (e) {
    err = '发送失败：' + e;
  }
  if (err) {
    authMsg(msgId, err, false);
    btn.disabled = false; // 没发出去就别让人干等 60 秒
    return;
  }
  authMsg(msgId, '验证码已发到邮箱，' + CODE_TTL_MIN + ' 分钟内有效', true);
  startCountdown(btn, 60);
}

if ($('btnSendLoginCode')) {
  $('btnSendLoginCode').onclick = () =>
    requestCode($('btnSendLoginCode'), 'loginMsg', 'login', $('loginEmail').value.trim(), '');
}

if ($('btnSendSuCode')) {
  $('btnSendSuCode').onclick = () =>
    requestCode($('btnSendSuCode'), 'suMsg', 'signup',
      $('suEmail').value.trim(), $('suCode').value.trim());
}

/* ---------- 登录 ---------- */

/* 现在选的是哪个 tab：'pass' 密码 / 'code' 验证码。
 *
 * 📌 **只认容器上的 data-mode 这一个来源** ——
 * 滑块的位置（CSS 看 data-mode）和"该走哪条登录路"（这里看它）必须同源，
 * 不然哪天改了滑块、忘了改判断，就会出现"高亮在验证码、实际走密码"这种鬼事 ✗ */
function loginMode() {
  const box = $('loginTabs');
  return (box && box.dataset.mode) || 'pass';
}

document.querySelectorAll('#loginTabs .ltab').forEach(t => {
  t.onclick = () => {
    const mode = t.dataset.lt;
    $('loginTabs').dataset.mode = mode; // 滑块跟着它滑过去（CSS 那边看这个）
    document.querySelectorAll('#loginTabs .ltab').forEach(x => x.classList.toggle('on', x === t));
    const isCode = mode === 'code';
    $('ltPassBox').hidden = isCode;
    $('ltCodeBox').hidden = !isCode;
    authMsg('loginMsg', '');
    ($(isCode ? 'loginCode' : 'loginPass') || $('loginEmail')).focus();
  };
});

async function doLogin() {
  const mode = loginMode();
  const isCode = mode === 'code';
  const fn = isCode ? window.LoginByCode : window.Login;
  if (!fn) { authMsg('loginMsg', '界面和后端没接上，重启一下试试', false); return; }

  const email = $('loginEmail').value.trim();
  const pass = $('loginPass').value;
  const code = $('loginCode').value.trim();
  if (!email) { authMsg('loginMsg', '请填邮箱', false); $('loginEmail').focus(); return; }
  if (isCode && !code) { authMsg('loginMsg', '请填验证码', false); $('loginCode').focus(); return; }
  if (!isCode && !pass) { authMsg('loginMsg', '请填密码', false); $('loginPass').focus(); return; }

  const btn = $('loginBtn');
  btn.disabled = true;
  authMsg('loginMsg', '正在登录…', null);
  try {
    const r = isCode ? await window.LoginByCode(email, code) : await window.Login(email, pass);
    if (!r || !r.ok) {
      // 失败文案直接用后端给的 —— 它在那边已经是"人话"了
      // （"邮箱或密码不对" / "验证码不对" / "这个账号已被停用" / "连不上服务器" …）
      authMsg('loginMsg', (r && r.error) || '登录失败', false);
      return;
    }
    // 🩸 跟注册一样：先确认"进来了"，再进主界面。
    //    直接跳进去的话，用户不确定刚才那次点击到底成没成 ✓
    showInfo('登录成功',
      '欢迎回来，' + r.email + '。',
      () => onLoggedIn(r.email));
  } catch (e) {
    authMsg('loginMsg', '登录失败：' + e, false);
  } finally {
    btn.disabled = false;
  }
}

if ($('loginBtn')) $('loginBtn').onclick = doLogin;

/* 回车就能提交 —— 登录页不认回车是会被人骂的 */
['loginEmail', 'loginPass', 'loginCode'].forEach(id => {
  const el = $(id);
  if (el) el.addEventListener('keydown', e => {
    if (e.key === 'Enter') { e.preventDefault(); doLogin(); }
  });
});

/* ---------- 注册 ---------- */

/* ---------- 通用提示弹窗 ----------
 *
 * 📌 有的是"说一句就好"的事（注册成功、密码已改），不该直接把人扔进主界面 ✓
 *    回调在"知道了"之后执行 —— 这样注册成功后是"先看到提示、再进软件" ✓ */
function showInfo(title, desc, after) {
  const m = $('infoModal');
  if (!m) { if (after) after(); return; }   // 万一没这个节点，别把人卡住
  $('infoTitle').textContent = title;
  $('infoDesc').textContent = desc;
  m.classList.add('show');
  $('infoOk').onclick = () => {
    m.classList.remove('show');
    if (after) after();
  };
  $('infoOk').focus();
}

async function doSignUp() {
  if (!window.SignUp) { authMsg('suMsg', '界面和后端没接上，重启一下试试', false); return; }
  const invite = $('suCode').value.trim();
  const email = $('suEmail').value.trim();
  const mailCode = $('suMailCode').value.trim();
  const pass = $('suPass').value;
  // 🩸 只有**需要邀请码**的模式才在这儿拦。
  //    开放注册时留空是合法的（后端也会跳过邀请码校验），
  //    这里再硬拦就变成"界面写着（可选）却不让人提交" ✗
  if (!invite && signupNeedsCode) {
    authMsg('suMsg', '请填邀请码', false);
    $('suCode').focus();
    return;
  }
  if (!email) { authMsg('suMsg', '请填邮箱', false); $('suEmail').focus(); return; }
  if (!/^\d{6}$/.test(mailCode)) {
    // 📌 先在本地挡住"位数不对"—— 省一次网络往返，用户也更快知道问题在哪 ✓
    authMsg('suMsg', '请填邮箱里收到的 6 位验证码', false);
    $('suMailCode').focus();
    return;
  }
  if (pass.length < 8) { authMsg('suMsg', '密码至少 8 位', false); $('suPass').focus(); return; }

  const btn = $('suBtn');
  btn.disabled = true;
  authMsg('suMsg', '正在注册…', null);
  try {
    const r = await window.SignUp(invite, email, pass, mailCode);
    if (!r || !r.ok) {
      authMsg('suMsg', (r && r.error) || '注册失败', false);
      return;
    }
    // 🩸 不直接进软件 —— 先告诉用户"注册成功了"，
    //    不然他会以为自己只是登录了一下，不知道账号已经建好 ✓
    showInfo('注册成功',
      '账号 ' + r.email + ' 已创建。现在可以创建房间或加入朋友的房间了。',
      () => onLoggedIn(r.email));
  } catch (e) {
    authMsg('suMsg', '注册失败：' + e, false);
  } finally {
    btn.disabled = false;
  }
}

if ($('suBtn')) $('suBtn').onclick = doSignUp;

['suCode', 'suEmail', 'suMailCode', 'suPass'].forEach(id => {
  const el = $(id);
  if (el) el.addEventListener('keydown', e => {
    if (e.key === 'Enter') { e.preventDefault(); doSignUp(); }
  });
});

/* ---------- 登录服务器地址 ----------
 *
 * 📌 它在**「设置」页**，不在登录页 —— 从登录页搬过来的。
 * 老板原话：「为什么底部要显示服务器和更改按钮？」说得对：
 * 登录页该只有"登录/注册"这一件事，别的一律不该在那儿 ✗
 * 但"能改"这个能力得留着 —— 服务器地址是最可能变的东西，
 * 钉死在 exe 里的话一换就得重发一遍客户端 ✓ */

function srvMsg(text, kind) {
  const el = $('srvMsg');
  if (!el) return;
  el.textContent = text || '';
  el.style.color = kind === true ? 'var(--ok)' : (kind === false ? 'var(--err)' : '');
}

/* 把当前地址刷到设置页那一行上 */
function renderSrvRow() {
  const now = $('srvNow');
  if (now) now.textContent = AUTH.server || '（没设置）';
}

renderSrvRow();

if ($('btnEditSrv')) $('btnEditSrv').onclick = () => {
  const row = $('srvEditRow');
  const box = $('srvInput');
  row.hidden = false;
  if (box) { box.value = AUTH.server || ''; box.focus(); }
  srvMsg('');
};

if ($('btnCancelSrv')) $('btnCancelSrv').onclick = () => {
  $('srvEditRow').hidden = true;
  srvMsg('');
};

async function saveSrv() {
  if (!window.SetLoginServer) { srvMsg('界面和后端没接上，重启一下试试', false); return; }
  const url = ($('srvInput').value || '').trim();
  const err = await window.SetLoginServer(url);
  if (err) { srvMsg(err, false); return; }
  // 后端存的是**规范化之后**的值（补协议、去尾斜杠）⇒ 以它为准重取一次，
  // 不然页面上显示的和你实际存的会长得不一样
  if (window.AccountState) {
    const st = await window.AccountState();
    if (st && st.server) AUTH.server = st.server;
  }
  renderSrvRow();
  $('srvEditRow').hidden = true;
  srvMsg('已保存', true);
}

if ($('btnSaveSrv')) $('btnSaveSrv').onclick = saveSrv;
if ($('srvInput')) $('srvInput').addEventListener('keydown', e => {
  if (e.key === 'Enter') { e.preventDefault(); saveSrv(); }
});

/* ---------- 退出登录 ---------- */

if ($('btnLogout')) {
  /* 不用弹窗确认：改成"再点一次"。
   * 两个理由：① 少一个弹窗就是少一次打断；② 这个动作**可逆**（重新登录就回来了）✓ */
  let armed = 0;
  $('btnLogout').onclick = async () => {
    const btn = $('btnLogout');
    if (armed) {
      clearTimeout(armed);
      armed = 0;
      btn.textContent = '退出登录';
      await window.Logout();
      AUTH.loggedIn = false;
      AUTH.email = '';
      applyAuthToProfile();
      setGated(true);
      show('login');
      authMsg('loginMsg', '已退出登录', true);
      return;
    }
    armed = setTimeout(() => { armed = 0; btn.textContent = '退出登录'; }, 3000);
    btn.textContent = '再点一次确认退出';
  };
}
