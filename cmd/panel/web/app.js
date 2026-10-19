'use strict';
/* 管理后台的前端。
 *
 * 两条纪律：
 *  ① **一律用 textContent，绝不拼 innerHTML** —— 邮箱、备注这些是用户可控的字符串，
 *     拼 HTML 就等于给自己开了个 XSS 口子。服务端那边 CSP 也是按"没有内联脚本"
 *     配的，两边是同一件事的两半 ✓
 *  ② 页面本身**不含任何数据**：它就是个壳，所有内容都现从 /api/admin/* 拿。
 *     所以"没登录就显示登录框"这件事由这里决定，服务端不用管 ✓
 */

const $ = (sel) => document.querySelector(sel);

// CSRF 令牌存内存，不落 localStorage：
// 落盘的话它就成了一个"能被脚本读走、还能跨标签页活很久"的东西 ✗
let csrf = '';

/** api 是所有请求的唯一出口。 */
async function api(path, opts = {}) {
  const init = {
    method: opts.method || 'GET',
    credentials: 'same-origin',
    headers: {},
  };
  if (opts.body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(opts.body);
  }
  if (csrf) init.headers['X-CSRF-Token'] = csrf;

  const res = await fetch(path, init);
  let data = null;
  const text = await res.text();
  if (text) {
    try { data = JSON.parse(text); } catch (e) { data = null; }
  }
  return { ok: res.ok, status: res.status, data };
}

/** call 在 api 之上加了一层"令牌过期就重拿一次再试"。
 *  不做这一层的话，服务端密钥一换，界面上每个按钮都会提示"刷新页面重试"，
 *  而用户根本不知道该刷新的是哪一层 ✗ */
async function call(path, opts) {
  let r = await api(path, opts);
  if (r.status === 403 && opts && opts.method === 'POST') {
    const s = await api('/api/admin/session');
    if (s.ok && s.data && s.data.csrf) {
      csrf = s.data.csrf;
      r = await api(path, opts);
    }
  }
  if (r.status === 401) showLogin();
  return r;
}

function msg(sel, text, kind) {
  const el = $(sel);
  el.textContent = text || '';
  el.className = 'msg' + (kind ? ' ' + kind : '');
}

function fmtTime(unix) {
  if (!unix) return '—';
  const d = new Date(unix * 1000);
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

function td(text, cls) {
  const c = document.createElement('td');
  if (cls) c.className = cls;
  c.textContent = text === undefined || text === null || text === '' ? '—' : text;
  return c;
}

function tag(text, cls) {
  const s = document.createElement('span');
  s.className = 'tag' + (cls ? ' ' + cls : '');
  s.textContent = text;
  return s;
}

// ── 登录 / 退出 ───────────────────────────────────────────

function showLogin() {
  csrf = '';
  $('#app').hidden = true;
  $('#login').hidden = false;
  $('#password').value = '';
}

function showApp(sess) {
  $('#login').hidden = true;
  $('#app').hidden = false;
  $('#whoami').textContent = sess.email || '';
  applyRoute(); // 按地址栏进对应的那一页（第一次进来地址是空的 ⇒ 补成默认页）
}

$('#loginForm').addEventListener('submit', async (e) => {
  // 🩸 preventDefault 不能省：真表单提交会整页跳转，
  // 而这是单页应用、跳走了状态就没了 ✗
  e.preventDefault();
  const btn = $('#loginBtn');
  btn.disabled = true;
  msg('#loginMsg', '正在登录…');
  const r = await api('/api/admin/login', {
    method: 'POST',
    body: { email: $('#email').value, password: $('#password').value },
  });
  btn.disabled = false;
  if (!r.ok) {
    msg('#loginMsg', (r.data && r.data.error) || '登录失败', 'err');
    return;
  }
  csrf = r.data.csrf;
  msg('#loginMsg', '');
  showApp(r.data);
});

$('#logout').addEventListener('click', async () => {
  await call('/api/admin/logout', { method: 'POST', body: {} });
  showLogin();
});

// ── 路由：每个菜单一个网址 ───────────────────────────────
//
// 老板 2026-10-16 提的：不要四个页面"挤在一处"，点一下要**跳到对应的页面** ✓
//
// 🩸 用 hash（`#/codes`）而不是真路径（`/admin/codes`）：hash 变化浏览器**不会**
// 去请求服务端 ⇒ 不用给服务端加四条路由 + 一条 SPA 兜底 ✗
// 而该有的一个不少：能收藏、刷新还停在这一页、后退/前进能走 ✓
//
// 📌 路由名跟菜单的 data-tab / 面板的 id **同名**（overview / codes / accounts / rooms），
// 不另做一张对照表 —— 三处各写一份映射，早晚有一处忘了改 ✗

const ROUTE_DEFAULT = 'overview';

function routeHash(tab, arg) { return arg ? '#/' + tab + '/' + arg : '#/' + tab; }

/** 地址栏里现在指的是哪一页（认不出来就返回空串）。 */
function routeTab() {
  const m = /^#\/([a-z]+)(?:\/([A-Za-z0-9-]+))?$/.exec(location.hash || '');
  return m ? m[1] : '';
}

/** 地址栏里的**参数**。现在只有房间详情用：`#/rooms/TI1234ABCD` 里那串房间号 ✓
 *
 *  📌 房间号只收 [A-Za-z0-9-] —— 它会被拼进查询串发给面板后端，
 *  再往后端会转发给信令侧。前端挡一道、后端再挡一道，两边都不省 ✓ */
function routeArg() {
  const m = /^#\/([a-z]+)\/([A-Za-z0-9-]+)$/.exec(location.hash || '');
  return m ? m[2] : '';
}

function $tabBtn(tab) { return $('#tabs button[data-tab="' + tab + '"]'); }

/** 只负责"显示谁、高亮谁" —— 发请求是另一件事（见 enterTab）。 */
function showTab(tab, arg) {
  const btn = $tabBtn(tab);
  for (const b of $('#tabs').children) b.classList.toggle('on', b === btn);
  // 房间详情是「房间」底下的**子页面**（不在侧栏菜单里）⇒
  // 它出现时把列表页藏起来，但侧栏高亮**仍然停在「房间」** ✓
  // （不然用户会觉得"我明明是从房间点进来的，菜单怎么不亮"✗）
  const target = (tab === 'rooms' && arg) ? 'tab-roomdetail'
    : (tab === 'history' && arg) ? 'tab-historydetail'
    : 'tab-' + tab;
  for (const p of document.querySelectorAll('.panel')) {
    p.hidden = p.id !== target;
  }
}

function enterTab(tab, arg) {
  showTab(tab, arg);
  if (tab === 'codes') loadCodes();
  if (tab === 'accounts') loadAccounts();
  if (tab === 'history') {
    if (arg) loadHistoryDetail(arg); else loadHistory();
  }
  if (tab === 'rooms') {
    if (arg) loadRoomDetail(arg); else loadRooms();
  }
  if (tab === 'overview') loadStats();
  if (tab === 'wall') loadWall();
  if (tab === 'settings') loadSettings();

  // 🩸 大屏要**满屏** ⇒ 这个 class 一加，侧栏就藏起来了（style.css 里那段）✓
  document.body.classList.toggle('wall', tab === 'wall');

  // 自动刷新的启停交给它（只在概览/大屏转，切走就停）✓
  ovApplyTimer();
}

/** 按地址栏走一遍。认不出来的地址 ⇒ 换成默认页（别留在一个空白页上）。 */
function applyRoute() {
  const tab = routeTab() || ROUTE_DEFAULT;
  const arg = routeArg();
  if (!$tabBtn(tab)) {
    location.replace(routeHash(ROUTE_DEFAULT));
    return;
  }
  if (location.hash !== routeHash(tab, arg)) {
    // 第一次进来地址是空的 ⇒ 用 replace 补上，不留一条多余的历史记录 ✓
    location.replace(routeHash(tab, arg));
  }
  enterTab(tab, arg);
}

// 点菜单：**只改地址**，切页交给下面的 hashchange ——
// 这样"点菜单"和"手动改地址/按后退"走的是同一条路，不会两套逻辑 ✗
$('#tabs').addEventListener('click', (e) => {
  const btn = e.target.closest('button[data-tab]');
  if (!btn) return;
  const h = routeHash(btn.dataset.tab);
  if (location.hash === h) return; // 点的就是当前页：不重复拉一次
  location.hash = h;
});

window.addEventListener('hashchange', applyRoute);

// ── 邀请码 ───────────────────────────────────────────────

$('#genBtn').addEventListener('click', async () => {
  const n = parseInt($('#genN').value, 10);
  if (!(n >= 1 && n <= 500)) {
    alert('数量要在 1 到 500 之间');
    return;
  }
  const r = await call('/api/admin/codes', {
    method: 'POST',
    body: { n: n, batch: $('#genBatch').value, note: $('#genNote').value },
  });
  if (!r.ok) { alert((r.data && r.data.error) || '生成失败'); return; }
  const list = (r.data.codes || []).map((c) => c.display);
  $('#genCount').textContent = String(list.length);
  $('#genList').textContent = list.join('\n');
  $('#genResult').hidden = false;
  loadCodes();
});

$('#copyBtn').addEventListener('click', async () => {
  try {
    await navigator.clipboard.writeText($('#genList').textContent);
    $('#copyBtn').textContent = '复制好了';
    setTimeout(() => { $('#copyBtn').textContent = '复制全部'; }, 1500);
  } catch (e) {
    alert('浏览器不让自动复制，手动选中复制吧');
  }
});

$('#reloadCodes').addEventListener('click', loadCodes);
$('#batchFilter').addEventListener('change', loadCodes);

$('#csvBtn').addEventListener('click', () => {
  const b = $('#batchFilter').value;
  // 用导航而不是 fetch：让浏览器自己处理"下载"这件事（含中文文件名）✓
  window.location.href = '/api/admin/codes.csv' + (b ? '?batch=' + encodeURIComponent(b) : '');
});

async function loadCodes() {
  const b = $('#batchFilter').value;
  const r = await call('/api/admin/codes' + (b ? '?batch=' + encodeURIComponent(b) : ''));
  if (!r.ok) return;
  const batches = r.data.batches || [];
  const sel = $('#batchFilter');
  if (sel.options.length - 1 !== batches.length) {
    sel.textContent = '';
    const all = document.createElement('option');
    all.value = '';
    all.textContent = '全部批号';
    sel.appendChild(all);
    for (const x of batches) {
      const o = document.createElement('option');
      o.value = x;
      o.textContent = x;
      sel.appendChild(o);
    }
    sel.value = b;
  }
  renderCodes(r.data.codes || []);
}

function renderCodes(rows) {
  const tb = $('#codesTable tbody');
  tb.textContent = '';
  if (!rows.length) {
    const tr = document.createElement('tr');
    const c = document.createElement('td');
    c.colSpan = 6;
    c.className = 'empty';
    c.textContent = '还没有邀请码。上面填个数量点「生成」。';
    tr.appendChild(c);
    tb.appendChild(tr);
    return;
  }
  for (const c of rows) {
    const tr = document.createElement('tr');
    tr.appendChild(td(c.display, 'mono'));
    const st = document.createElement('td');
    st.appendChild(tag(c.state === 'used' ? '已使用' : '未使用', c.state === 'used' ? 'used' : ''));
    tr.appendChild(st);
    tr.appendChild(td(c.batch));
    tr.appendChild(td(c.note));
    tr.appendChild(td(c.usedEmail));
    tr.appendChild(td(c.state === 'used'
      ? fmtTime(c.usedAt) + ' 用'
      : fmtTime(c.createdAt) + ' 生成'));
    const op = document.createElement('td');
    op.className = 'actions';
    const b = document.createElement('button');
    b.className = 'ghost';
    b.textContent = '改备注';
    b.addEventListener('click', () => editNote(c));
    op.appendChild(b);
    tr.appendChild(op);
    tb.appendChild(tr);
  }
}

async function editNote(c) {
  const note = prompt('这条备注写什么（"发给谁"）？留空就是清掉。', c.note || '');
  if (note === null) return;
  const r = await call('/api/admin/codes/note', { method: 'POST', body: { code: c.code, note: note } });
  if (!r.ok) { alert((r.data && r.data.error) || '改备注失败'); return; }
  loadCodes();
}

// ── 账号 ─────────────────────────────────────────────────

$('#reloadAccounts').addEventListener('click', loadAccounts);

async function loadAccounts() {
  const r = await call('/api/admin/accounts');
  if (!r.ok) return;
  renderAccounts(r.data.accounts || []);
}

function renderAccounts(rows) {
  const tb = $('#accountsTable tbody');
  tb.textContent = '';
  if (!rows.length) {
    const tr = document.createElement('tr');
    const c = document.createElement('td');
    c.colSpan = 8;
    c.className = 'empty';
    c.textContent = '还没有账号。把邀请码发出去，朋友注册之后就会出现在这儿。';
    tr.appendChild(c);
    tb.appendChild(tr);
    return;
  }
  for (const a of rows) {
    const tr = document.createElement('tr');
    tr.appendChild(td(String(a.id)));
    tr.appendChild(td(a.email));
    const st = document.createElement('td');
    st.appendChild(tag(a.status === 'active' ? '正常' : '已停用', a.status === 'active' ? '' : 'disabled'));
    tr.appendChild(st);
    tr.appendChild(td(a.role === 'admin' ? '管理员' : '用户'));
    tr.appendChild(td(a.code, 'mono'));
    tr.appendChild(td(fmtTime(a.createdAt)));
    tr.appendChild(td(String(a.sessions)));

    const op = document.createElement('td');
    op.className = 'actions';
    if (a.role !== 'admin') {
      op.appendChild(act(a.status === 'active' ? '停用' : '恢复', 'danger', async () => {
        const r = await call('/api/admin/accounts/status', {
          method: 'POST',
          body: { id: a.id, status: a.status === 'active' ? 'disabled' : 'active' },
        });
        if (!r.ok) alert((r.data && r.data.error) || '操作失败');
        loadAccounts();
      }));
    }
    op.appendChild(act('强制下线', 'ghost', async () => {
      const r = await call('/api/admin/accounts/revoke', { method: 'POST', body: { id: a.id } });
      if (!r.ok) { alert((r.data && r.data.error) || '操作失败'); return; }
      loadAccounts();
    }));
    tr.appendChild(op);
    tb.appendChild(tr);
  }
}

function act(text, cls, fn) {
  const b = document.createElement('button');
  b.className = cls;
  b.textContent = text;
  b.addEventListener('click', fn);
  return b;
}

// ── 房间 ─────────────────────────────────────────────────

$('#reloadRooms').addEventListener('click', loadRooms);

/** roomGameFilter 是房间列表的游戏筛选：
 *  空串 = 全部；'__none__' = 未上报（**它不是一个游戏名**，是"我们不知道"）✓ */
let roomGameFilter = '';

/** gameCell 游戏那一格：有名字就是一个圆角标签，没有就是虚线"未上报"。
 *
 *  🩸 "未上报"必须跟真游戏长得**不一样** —— 它是"老客户端没发这个字段"，
 *  不是某一款游戏。混在一起看，会让人以为真有款游戏叫"未上报"✗ */
function gameCell(g) {
  const c = document.createElement('td');
  const sp = document.createElement('span');
  if (g) {
    sp.className = 'game';
    sp.textContent = g;
  } else {
    sp.className = 'game none';
    sp.textContent = '未上报';
  }
  c.appendChild(sp);
  return c;
}

/** fmtBytes 把字节数收成 B / KB / MB。
 *  📌 信令流量本来就很轻（游戏数据不走服务器）⇒ 小到几十字节也得看得见，
 *  一律按 KB 显示会全是 "0.0 KB"，那这个数就白给了 ✗ */
function fmtBytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return chNum(n / 1024) + ' KB';
  return chNum(n / 1024 / 1024) + ' MB';
}

/** fillGameFilter 按**当前真实出现过的**游戏名填下拉。
 *
 *  📌 不写死一份清单：那样加了新游戏还得改面板，两边迟早对不上 ✗
 *  ⚠️ 只在"选项集合真的变了"时才重建 —— 每秒重建一次会把用户
 *  正展开的下拉关掉、也会把选中项弄丢 ✗ */
function fillGameFilter(rooms) {
  const sel = $('#gameFilter');
  if (!sel) return;
  const names = [];
  let hasNone = false;
  for (const x of rooms) {
    if (!x.game) { hasNone = true; continue; }
    if (names.indexOf(x.game) < 0) names.push(x.game);
  }
  names.sort();
  const want = [''].concat(names).concat(hasNone ? ['__none__'] : []);
  const have = [].map.call(sel.options, (o) => o.value);
  if (have.join('|') === want.join('|')) return;
  sel.textContent = '';
  sel.appendChild(new Option('全部游戏', ''));
  for (const n of names) sel.appendChild(new Option(n, n));
  if (hasNone) sel.appendChild(new Option('未上报', '__none__'));
  // 重建之后把选中项还回去（放的还是原来那个；它没了就退回"全部"）✓
  sel.value = want.indexOf(roomGameFilter) >= 0 ? roomGameFilter : '';
  roomGameFilter = sel.value;
}

async function loadRooms() {
  // 房间那条记录里只有**账号号**；邮箱在账号列表里 ⇒ 两个一起拿，本地对一下。
  // 📌 为什么不在服务端拼：房间快照是信令服务算的，账号表在面板这边 ——
  // 让信令服务去查账号表就把"那两个包各管各的"这条分工搅乱了 ✗
  const [r, ra] = await Promise.all([call('/api/admin/rooms'), call('/api/admin/accounts')]);
  if (!r.ok) return;
  const mail = {};
  if (ra && ra.ok && ra.data) {
    for (const a of (ra.data.accounts || [])) mail[a.id] = a.email;
  }
  const tb = $('#roomsTable tbody');
  tb.textContent = '';
  if (!r.data.available) {
    msg('#roomsMsg', r.data.error || '取不到房间列表', 'err');
    return;
  }
  msg('#roomsMsg', '');
  const all = (r.data.data && r.data.data.rooms) || [];
  fillGameFilter(all);

  const rooms = all.filter((x) => {
    if (!roomGameFilter) return true;
    if (roomGameFilter === '__none__') return !x.game;
    return x.game === roomGameFilter;
  });
  const cnt = $('#roomsCount');
  if (cnt) {
    cnt.textContent = roomGameFilter
      ? ('筛出 ' + rooms.length + ' / 共 ' + all.length + ' 个房间')
      : ('共 ' + all.length + ' 个房间');
  }

  if (!rooms.length) {
    const tr = document.createElement('tr');
    const c = document.createElement('td');
    c.colSpan = 8;
    c.className = 'empty';
    c.textContent = all.length ? '这个筛选下没有房间。' : '现在没有人在开房间。';
    tr.appendChild(c);
    tb.appendChild(tr);
    return;
  }
  for (const x of rooms) {
    const tr = document.createElement('tr');
    tr.className = 'clickable';
    // 🩸 整行可点：点进去看成员明细。
    // 「关闭」那个按钮也在行里 ⇒ 点按钮时**别顺带钻进详情页** ✗
    tr.addEventListener('click', (ev) => {
      if (ev.target.closest('button')) return;
      location.hash = routeHash('rooms', x.room);
    });
    tr.appendChild(td(x.room, 'mono'));
    tr.appendChild(gameCell(x.game));
    tr.appendChild(td(x.members + ' / ' + x.maxSize));
    tr.appendChild(td(x.host));
    tr.appendChild(td(x.accountId ? (mail[x.accountId] || '账号 #' + x.accountId) : '—（没登录）'));
    tr.appendChild(td(fmtDur(x.openSeconds)));
    tr.appendChild(td(x.from, 'mono'));
    const op = document.createElement('td');
    op.className = 'actions';
    op.appendChild(act('关闭', 'ghost danger', () => closeRoom(x)));
    tr.appendChild(op);
    tb.appendChild(tr);
  }
}

// ── 房间详情（#/rooms/房间号）──────────────────────────────

async function loadRoomDetail(code) {
  const [r, ra] = await Promise.all([
    call('/api/admin/rooms/detail?room=' + encodeURIComponent(code)),
    call('/api/admin/accounts'),
  ]);
  if (!r.ok) return;
  const mail = {};
  if (ra && ra.ok && ra.data) {
    for (const a of (ra.data.accounts || [])) mail[a.id] = a.email;
  }
  if (!r.data || !r.data.available) {
    msg('#rdMsg', (r.data && r.data.error) || '取不到房间详情', 'err');
    // 🩸 房间没了（多半是刚被关掉）⇒ **把内容清掉**。
    // 不清的话屏幕上还留着上一个房间的成员名单，而标题写着新房间号
    // —— 那种"看着是这间、其实是那间"的错最误导人 ✗
    $('#rdCards').textContent = '';
    const tb0 = $('#rdMembers tbody');
    if (tb0) tb0.textContent = '';
    return;
  }
  msg('#rdMsg', '');
  renderRoomDetail(r.data.data || {}, mail);
}

function renderRoomDetail(d, mail) {
  const mem = d.members || [];
  $('#rdTitle').textContent = '房间 ' + d.room;
  $('#rdSub').textContent = '游戏 ' + (d.game || '未上报') +
    ' · 开了 ' + fmtDur(d.openSeconds) +
    ' · 成员 ' + mem.length + ' / ' + d.maxSize +
    ' · 每秒刷新';

  const cards = $('#rdCards');
  cards.textContent = '';
  const acc = d.accountId ? (mail[d.accountId] || ('账号 #' + d.accountId)) : '—（没登录）';
  cards.appendChild(ovCard({ k: '房间 ID', v: d.room }));
  cards.appendChild(ovCard({ k: '游戏', v: d.game || '未上报' }));
  cards.appendChild(ovCard({ k: '人数', v: String(mem.length), u: '/ ' + d.maxSize + ' 人' }));
  cards.appendChild(ovCard({ k: '开了多久', v: fmtDur(d.openSeconds) }));
  cards.appendChild(ovCard({ k: '房主来源', v: d.from }));
  cards.appendChild(ovCard({ k: '开的账号', v: acc }));
  // 📌 只显示"有没有设密码"，**绝不显示密码本身** —— 面板上没有它的用途，
  // 显示了只是多一处泄露面 ✗
  cards.appendChild(ovCard({ k: '房间密码', v: d.hasPass ? '有' : '无' }));
  cards.appendChild(ovCard({ k: '黑名单', v: String(d.banCount || 0), u: '条' }));

  const tb = $('#rdMembers tbody');
  tb.textContent = '';
  if (!mem.length) {
    const tr = document.createElement('tr');
    const c = document.createElement('td');
    c.colSpan = 7;
    c.className = 'empty';
    c.textContent = '房间里现在没有人（可能刚散）。';
    tr.appendChild(c);
    tb.appendChild(tr);
    return;
  }
  for (const m of mem) {
    const tr = document.createElement('tr');
    tr.appendChild(td(m.host ? '房主' : ('玩家 ' + m.seat)));
    tr.appendChild(td(m.label || '（没起名）'));
    tr.appendChild(gameCell(m.game));
    tr.appendChild(td(m.from, 'mono'));
    tr.appendChild(td(fmtDur(m.joinSeconds)));
    tr.appendChild(td(fmtBytes(m.rxBytes) + ' / ' + fmtBytes(m.txBytes), 'mono'));
    const op = document.createElement('td');
    op.className = 'actions';
    if (m.host) {
      // 🩸 房主那行**不给**踢人按钮：要停就关整个房间 ✓
      // （后端也拦着 —— 这儿只是不给一个按了会报错的按钮）
      op.textContent = '—';
    } else {
      op.appendChild(act('踢出', 'ghost danger', () => kickMember(d.room, m)));
    }
    tr.appendChild(op);
    tb.appendChild(tr);
  }
}

/** kickMember 把一个成员请出房间。
 *
 *  🩸 二次确认不能省：这会让对方**当场掉线**（他在打的游戏会话会断）✗
 *  📌 跟关房间一样：前端这个确认只是"别手滑"，真正的判定在服务端 ✓ */
async function kickMember(code, m) {
  const who = m.label || ('玩家 ' + m.seat);
  const yes = confirm(
    '把「' + who + '」请出房间 ' + code + ' ？\n\n' +
    '对方会当场断开，房间继续开着（其他人不受影响）。\n' +
    '他随时可以再进来（除非房主把他拉黑了）。');
  if (!yes) return;
  const r = await call('/api/admin/rooms/kick',
    { method: 'POST', body: { room: code, seat: m.seat } });
  if (!r.ok) {
    alert((r.data && r.data.error) || '踢不掉');
    return;
  }
  loadRoomDetail(code); // 重新拉一遍：那个人该从名单里消失了
}

/** closeRoom 从后台强制关掉一个房间（把里面的人断开）。
 *
 * 🩸 **二次确认不能省**：这个动作不可逆，而且伤的是正在玩的人 ✗
 * （房主随时能重开，但房里那几个人的游戏会话是直接断的）
 *
 * 📌 判定在服务端：前端这个确认只是"别手滑"，不是防线 ✓
 */
async function closeRoom(x) {
  const n = Math.max(1, Number(x.members) || 1);
  // 从详情页调过来时拿不到人数（members 传的是 null）⇒ 那种情况说"房里所有人"，
  // 别硬写"1 个人会被断开" —— 那是在说一个我们**并不知道**的数 ✗
  const who = x.members ? ('房里 ' + n + ' 个人会被断开') : '房里所有人会被断开';
  const yes = confirm(
    '关掉房间 ' + x.room + ' ？\n\n' +
    who + '（含房主），房间立刻消失。\n' +
    '房主随时可以重新开一个。\n\n' +
    '（房主和玩家那边会看到「管理员关闭了这个房间」）');
  if (!yes) return;
  const r = await call('/api/admin/rooms/close', { method: 'POST', body: { room: x.room } });
  if (!r.ok) {
    alert((r.data && r.data.error) || '关不掉');
    return;
  }
  loadRooms(); // 重新拉一遍：关掉的那个该从列表里消失了
}

function fmtDur(sec) {
  sec = Math.max(0, Math.round(sec || 0));
  const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60), s = sec % 60;
  if (h) return h + ' 小时 ' + m + ' 分';
  if (m) return m + ' 分 ' + s + ' 秒';
  return s + ' 秒';
}

// ── 概览 / 大屏：同一份数据、同一套渲染 ───────────────────
//
// 数据来自 /api/admin/metrics —— 信令侧每 1 秒采一个点
// （CPU / 内存 / 带宽 / 连接 / 房间 / 拒绝），内存留 6 小时、同时追加落盘 ✓
//
// 📌 **概览页 和 大屏 画的是同一份数据**，只有容器和字号不同 ⇒
// 渲染只写一份（renderOverview），两边共用。
// 写两份的话，以后加一个指标要改两个地方、早晚漏一个 ✗

let ovRange = 3600;   // 时间范围（秒）—— 两个页面共用
let ovTimer = null;   // 自动刷新的定时器
let ovAuto = true;    // 用户想不想自动刷新

const OV = { cards: '#statCards', charts: '#charts', updated: '#updatedAt', msg: '#statsMsg' };
const WALL = { cards: '#wallCards', charts: '#wallCharts', updated: '#wallUpdated', msg: '#wallMsg' };

/** ovTrend 跟上一个采样点比，涨还是跌。
 *  ⚠️ 用中性色 —— CPU 涨不是坏事，涂成红的反而误导 ✗
 *  asInt：**计数类**（连接 / 房间）用整数 —— 印出 "↑ 3.57 个连接" 很业余 ✗ */
function ovTrend(cur, prev, asInt) {
  if (prev === null || prev === undefined) return '';
  const d = cur - prev;
  const eps = asInt ? 0.5 : 0.05;   // 计数至少变了 1 才值得标个箭头
  if (!isFinite(d) || Math.abs(d) < eps) return '→';
  return (d > 0 ? '↑ ' : '↓ ') + (asInt ? chInt(Math.abs(d)) : chNum(Math.abs(d)));
}

/** ovTone 按阈值给卡片定调：>=90 红、>=80 黄，其余中性。
 *  ⚠️ **只喂"占上限的比例"**：房间数 / 房间上限 可以；连接数 100 个本身不代表紧张 ✗ */
function ovTone(pct) {
  if (!isFinite(pct)) return '';
  if (pct >= 90) return 'bad';
  if (pct >= 80) return 'warn';
  return '';
}

function ovCard(c) {
  const d = document.createElement('div');
  d.className = 'stat' + (c.tone ? ' ' + c.tone : '');
  const k = document.createElement('div');
  k.className = 'k';
  k.textContent = c.k;
  const v = document.createElement('div');
  v.className = 'v';
  v.appendChild(document.createTextNode(c.v));
  if (c.u) {
    const u = document.createElement('span');
    u.className = 'u';
    u.textContent = c.u;
    v.appendChild(u);
  }
  if (c.trend) {
    const tr = document.createElement('span');
    tr.className = 'trend';
    tr.textContent = c.trend;
    v.appendChild(tr);
  }
  d.appendChild(k);
  d.appendChild(v);
  if (c.sub) {
    const sb = document.createElement('div');
    sb.className = 'sub';
    sb.textContent = c.sub;
    d.appendChild(sb);
  }
  return d;
}

const CH = { us: 'var(--accent)', them: 'var(--ok)', warn: 'var(--warn)', red: 'var(--danger)' };

/** clockOf 把 unix 秒写成 HH:MM（本地时区）。没值就给 "—"，不编一个假的 ✗ */
function clockOf(sec) {
  if (!sec) return '—';
  const x = new Date(sec * 1000);
  const p = (n) => String(n).padStart(2, '0');
  return p(x.getHours()) + ':' + p(x.getMinutes());
}

/** stampOf 把 ISO 时间串（信令那边的 `vcs.time`，**带 Z 是 UTC**）写成
 *  "年-月-日 时:分"（本地时区）。
 *
 * 🩸 必须过一遍 Date 再显示：直接 slice 出来的 "11:10" 是 **UTC**，
 * 老板会当成本地时间看 ⇒ 差 8 小时，然后怀疑"这时间不对吧" ✗ */
function stampOf(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d.getTime())) return String(iso).slice(0, 16).replace('T', ' ');
  const p = (n) => String(n).padStart(2, '0');
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) +
    ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
}

/** renderOverview 把一份 /metrics 数据铺进指定的那组容器。
 *
 *  ① 卡片 **12 张**（4 的倍数 ⇒ 宽屏 4×3 正好排满，不会"最后一张掉下来"✗）
 *  ② 折线图 **6 张**（3 列正好 3×2 ✓）
 *  ③ **单位一律走 u 字段**（小号字），不拼进大数字里 ✓
 *  ④ 采不到 / 没值的字段显示 "—"，**不显示 0** —— 0 会被当成"很闲"，是假话 ✗ */
function renderOverview(sel, d) {
  const cards = $(sel.cards);
  const charts = $(sel.charts);
  if (!cards || !charts) return;

  const L = d.latest || {};
  const lim = d.limits || {};
  const idn = d.identity || {};
  const T = d.t || [];
  const stepSec = d.step || 10;
  const prevOf = (arr, k) => (arr && arr.length > k ? arr[arr.length - 1 - k] : null);
  const cpus = lim.cpus || 0;

  // ── 卡片 ────────────────────────────────────────────────
  cards.textContent = '';

  // ① 资源
  const [rssV, rssU] = chBytes(L.rss);
  cards.appendChild(ovCard({
    k: 'CPU · 我们', v: chNum(L.pcpu), u: '%',
    tone: ovTone(L.pcpu), trend: ovTrend(L.pcpu, prevOf(d.pcpu, 1)),
    sub: '占满一个核 = 100%' + (cpus ? ' · 这台机器 ' + cpus + ' 个核' : ''),
  }));

  cards.appendChild(ovCard({
    k: '内存 · 我们', v: rssV, u: rssU,
    trend: ovTrend(L.rss, prevOf(d.rss, 1)),
    sub: '常驻内存（信令服务这一份）',
  }));

  cards.appendChild(ovCard({
    k: '带宽 · 我们', v: chNum(L.orx) + ' / ' + chNum(L.otx), u: 'KB/s',
    sub: '收 / 发 · 在连接上数的，只算这个服务本身',
  }));

  cards.appendChild(ovCard({
    k: '本次运行', v: fmtDur(lim.uptime_seconds),
    sub: '启动于 ' + clockOf(idn.started_at),
  }));

  // ② 负载
  cards.appendChild(ovCard({
    k: '当前连接', v: chInt(L.conns), u: '个',
    trend: ovTrend(L.conns, prevOf(d.conns, 1), true),
    sub: '峰值 ' + chInt(lim.conns_peak) + ' · 每 IP 上限 ' + chInt(lim.conns_per_ip),
  }));

  cards.appendChild(ovCard({
    k: '当前房间', v: chInt(L.rooms), u: '个',
    trend: ovTrend(L.rooms, prevOf(d.rooms, 1), true),
    tone: lim.max_rooms ? ovTone((L.rooms / lim.max_rooms) * 100) : '',
    sub: '峰值 ' + chInt(lim.rooms_peak) + ' · 上限 ' + chInt(lim.max_rooms),
  }));

  cards.appendChild(ovCard({
    k: '累计开过的房间', v: chInt(lim.rooms_total), u: '个',
    sub: '本次启动以来（信令重启会归零）',
  }));

  // ③ 安全
  cards.appendChild(ovCard({
    k: '被挡掉的', v: chInt((L.rej || 0) + (lim.reject_ban || 0)), u: '次',
    sub: '限流 ' + chInt(L.rej) + ' · 拉黑 ' + chInt(lim.reject_ban),
  }));

  cards.appendChild(ovCard({
    k: '令牌不认', v: chInt(L.bad), u: '次',
    sub: '无效 / 过期 / 被踢下线 —— 有人拿旧令牌来连',
  }));

  cards.appendChild(ovCard({
    k: '没带令牌', v: chInt(lim.no_token), u: '次',
    sub: '匿名连接尝试（没登录就来连）',
  }));

  // ④ 采样与身份
  const keepH = Math.round((lim.metrics_keep || 0) / 360) / 10;
  cards.appendChild(ovCard({
    k: '指标保留', v: String(keepH), u: '小时',
    sub: '每 ' + (lim.metrics_interval || 1) + ' 秒一个点 · 内存里 + 追加落盘',
  }));

  // 服务身份：挂屏 / 排查时**一眼看出"这是哪个版本在跑"** ✓
  // 📌 拿不到就显示 "—"：宁可空白也别编一个版本号 ✗
  // ⚠️ 哈希只显示 8 位（33px 的字号下 12 位会被折行）；"有未提交改动"要单独标出来 ——
  //    那种二进制跟那个提交**不是一回事**，不说明的话排查时会拿着它去对代码 ✗
  const revRaw = idn.revision || '';
  const rev = revRaw.split('+')[0];
  const dirty = revRaw.indexOf('+') >= 0;
  cards.appendChild(ovCard({
    k: '服务版本', v: rev ? rev.slice(0, 8) : '—',
    sub: (idn.built_at ? '提交于 ' + stampOf(idn.built_at) : '没有构建信息') +
      (idn.go_version ? ' · ' + idn.go_version : '') +
      (dirty ? ' · ⚠️ 编的时候有未提交改动' : ''),
  }));

  // ── 折线图 ──────────────────────────────────────────────
  charts.textContent = '';
  const mk = (title, unit, series, opts) => {
    const box = document.createElement('div');
    box.className = 'chartbox';
    lineChart(box, Object.assign({ title: title, unit: unit, series: series, t: T }, opts || {}));
    charts.appendChild(box);
  };

  mk('CPU · 我们', '%',
    [{ name: '占用', color: CH.us, data: d.pcpu || [], fill: true }],
    { pct: true, warn: 80 });

  mk('内存 · 我们', 'MB',
    [{ name: '常驻', color: CH.us, data: d.rss || [], fill: true }], {});

  mk('带宽 · 我们', 'KB/s', [
    { name: '收', color: CH.us, data: d.orx || [], fill: true },
    { name: '发', color: CH.them, data: d.otx || [] },
  ], {});

  mk('连接数', '个',
    [{ name: '连接', color: CH.us, data: d.conns || [], fill: true }], { int: true });

  mk('房间数', '个',
    [{ name: '房间', color: CH.warn, data: d.rooms || [], fill: true }], { int: true });

  // 🩸 拒绝那条要**换算成"次/分"**再画：后端给的是"每个采样周期多少次"，
  // 切了时间范围之后 step 会变 ⇒ 不换算的话，同一件事在 5 分钟和 6 小时图上
  // 高度差好几倍，看着像"突然变严重了" ✗ 换算完就跟范围无关了 ✓
  const perMin = 60 / stepSec;
  mk('拒绝与安全', '次/分', [
    { name: '限流', color: CH.warn, data: (d.rej || []).map((v) => v * perMin), fill: true },
    { name: '令牌不认', color: CH.red, data: (d.bad || []).map((v) => v * perMin) },
  ], { int: true });

  // ── 右上角：最后更新 ────────────────────────────────────
  const upd = $(sel.updated);
  if (upd) {
    const now = new Date();
    const p2 = (x) => String(x).padStart(2, '0');
    // 🩸 把「采样多密」和「图上每点代表多久」分开说 ——
    // 只说后者（"每 10 秒一个"）会让人以为采样是 10 秒，跟卡片上"每 1 秒"打架 ✗
    upd.textContent = '最后更新 ' + p2(now.getHours()) + ':' + p2(now.getMinutes()) + ':' +
      p2(now.getSeconds()) + '（采样每 ' + (lim.metrics_interval || 1) + ' 秒 · 图上 ' +
      T.length + ' 个点，每点约 ' + stepSec + ' 秒）';
  }
}

/** ovFetch 拉一次数据并铺到指定容器。取不到就说清原因，不许留一片空白 ✗ */
async function ovFetch(sel) {
  const r = await call('/api/admin/metrics?range=' + ovRange);
  if (!r.ok) return;
  if (!r.data || !r.data.available) {
    msg(sel.msg, (r.data && r.data.error) || '取不到指标（穿透服务器那边没开这个端点？）', 'err');
    return;
  }
  msg(sel.msg, '');
  renderOverview(sel, r.data.data || {});
}

function loadStats() { return ovFetch(OV); }
function loadWall() { return ovFetch(WALL); }

/** ovTick 定时器回调：按**当前在哪一页**决定拉哪个（不在那两页就啥也不干）✓ */
function ovTick() {
  const tab = routeTab() || ROUTE_DEFAULT;
  const arg = routeArg();
  if (tab === 'overview') loadStats();
  else if (tab === 'wall') loadWall();
  // 房间那两页也要跟秒：列表的「开了多久」、详情的「加入多久」都在走 ✓
  else if (tab === 'rooms') {
    if (arg) loadRoomDetail(arg); else loadRooms();
  }
}

/** syncAutoButtons 两个「自动刷新」按钮的文字得一致 ——
 *  它们共用 ovAuto 这一个开关；只更新一个的话，另一个会显示错的状态 ✗ */
function syncAutoButtons() {
  for (const id of ['#autoRefresh', '#autoRooms']) {
    const b = $(id);
    if (b) b.textContent = '自动刷新：' + (ovAuto ? '开' : '关');
  }
}

/** ovApplyTimer 按"用户想不想要 + 现在在哪一页"决定定时器转不转。
 *  🩸 切到别的页面还一直拉纯属白费 —— 那是老板看不见的请求 ✗ */
function ovApplyTimer() {
  if (ovTimer) { clearInterval(ovTimer); ovTimer = null; }
  const tab = routeTab() || ROUTE_DEFAULT;
  // ⏱ 一秒一次（跟采样间隔一样）—— 老板 2026-10-16 晚："怎么不是按秒刷新啊，
  // 刷新这么慢"。📌 两边必须一起改：采样 10 秒的话，前端每秒拉也只是看同一个数 ✗
  // 📌 概览 / 大屏 / **房间（列表与详情）** 都要按秒刷 ✓
  if (ovAuto && (tab === 'overview' || tab === 'wall' || tab === 'rooms')) {
    ovTimer = setInterval(ovTick, 1000);
  }
  syncAutoButtons();
}

/** setRange 切时间范围。**两个页面的按钮一起同步** —— 范围是共用的，
 *  只亮一个的话另一个页面会显示"选着 1 小时"其实在拉 5 分钟 ✗ */
function setRange(sec) {
  ovRange = sec;
  const bars = document.querySelectorAll('.ranges');
  for (const bar of bars) {
    for (const b of bar.querySelectorAll('.rbtn')) {
      b.classList.toggle('on', Number(b.dataset.range) === sec);
    }
  }
  const tab = routeTab() || ROUTE_DEFAULT;
  if (tab === 'wall') loadWall(); else loadStats();
}

for (const bar of document.querySelectorAll('.ranges')) {
  bar.addEventListener('click', (e) => {
    const b = e.target.closest('.rbtn');
    if (!b) return;
    setRange(Number(b.dataset.range) || 3600);
  });
}

if ($('#autoRefresh')) {
  $('#autoRefresh').addEventListener('click', () => {
    ovAuto = !ovAuto;
    $('#autoRefresh').textContent = '自动刷新：' + (ovAuto ? '开' : '关');
    ovApplyTimer();
  });
}

if ($('#reloadStats')) {
  $('#reloadStats').addEventListener('click', loadStats);
}

// ── 房间页：自动刷新开关 / 刷新 / 返回列表 / 关掉整个房间 / 游戏筛选 ──
if ($('#autoRooms')) {
  $('#autoRooms').addEventListener('click', () => {
    ovAuto = !ovAuto;
    ovApplyTimer();
  });
}

if ($('#reloadRooms')) {
  $('#reloadRooms').addEventListener('click', () => {
    const arg = routeArg();
    if (routeTab() === 'rooms' && arg) loadRoomDetail(arg); else loadRooms();
  });
}

if ($('#gameFilter')) {
  $('#gameFilter').addEventListener('change', () => {
    roomGameFilter = $('#gameFilter').value;
    loadRooms();
  });
}

if ($('#rdBack')) {
  $('#rdBack').addEventListener('click', () => { location.hash = routeHash('rooms'); });
}

if ($('#rdClose')) {
  // 详情页里那个「关闭整个房间」跟列表里的「关闭」是同一个动作 ⇒ 复用同一个函数 ✓
  $('#rdClose').addEventListener('click', () => {
    const code = routeArg();
    if (code) closeRoom({ room: code, members: null });
  });
}

// ── 启动 ─────────────────────────────────────────────────

(async function boot() {
  const r = await api('/api/admin/session');
  if (r.ok && r.data && r.data.csrf) {
    csrf = r.data.csrf;
    showApp(r.data);
  } else {
    showLogin();
  }
})();

// ── 设置（2026-10-17 加：注册是否需要邀请码）─────────────────
//
// 📌 界面上是「注册需要邀请码」（正向），后端存的是 signupOpen（也是正向，但反义）——
//    两边都用"正向"是为了**默认值落在安全那侧**：
//    后端 signupOpen 默认 false = 要码 ✓；界面没加载出来时 checkbox 默认没勾 = 要码 ✓

/** 拉当前设置，把界面拨到对应状态。 */
async function loadSettings() {
  const r = await call('/api/admin/settings');
  if (!r.ok) return;
  $('#setSignupNeedsCode').checked = !r.data.signupOpen;
  setSetMsg('');
}

function setSetMsg(text, kind) {
  const el = $('#setMsg');
  if (!el) return;
  el.textContent = text;
  el.className = 'setmsg' + (kind ? ' ' + kind : '');
}

// 开关**一拨就存**（单开关的页再加个"保存"按钮，反而要多想一步 ✗）
document.addEventListener('change', async (e) => {
  if (!e.target || e.target.id !== 'setSignupNeedsCode') return;
  const needsCode = e.target.checked;
  e.target.disabled = true;
  setSetMsg('正在保存…');
  const r = await call('/api/admin/settings', {
    method: 'POST',
    body: { signupOpen: !needsCode },
  });
  e.target.disabled = false;
  if (!r.ok) {
    // 🩸 失败要**回滚界面**，不然界面跟服务器说的不是一回事 ✗
    e.target.checked = !needsCode;
    setSetMsg((r.data && r.data.error) || '保存失败', 'err');
    return;
  }
  setSetMsg(needsCode ? '已保存：注册需要邀请码' : '已保存：注册不需要邀请码', 'ok');
});

// ── 历史房间 ────────────────────────────────────────────────
//
// 数据来自 /api/admin/rooms/history（面板转发给信令服务的 /history）。
// 那份记录是**服务器侧**留的、只留 30 天 —— 页头上写明了，
// 别让人以为能查到"去年那个房间" ✗
//
// 📌 全部用 textContent 拼，不拿 innerHTML 塞昵称：昵称是用户自己起的字 ✗

let historyRows = [];
let historyMail = {};   // 账号 ID → 邮箱（列表加载时顺手建好，导出也用）

// 离开原因：后端给的是机器可读的英文，这里翻成人话
const LEAVE_WHY = {
  left: '退出',
  gone: '掉线',
  kicked: '被踢',
  banned: '被拉黑',
  admin: '管理员移出',
};

function loadHistory() {
  const q = (($('#historyQ') && $('#historyQ').value) || '').trim();
  const url = '/api/admin/rooms/history' + (q ? '?q=' + encodeURIComponent(q) : '');
  // 🩸 历史里存的是**账号 ID**（日志文件里不放邮箱）。
  // 要显示邮箱就得连账号表一起拿、本地对一下 —— 跟 loadRooms 一个套路 ✓
  return Promise.all([call(url), call('/api/admin/accounts')]).then(([r, ra]) => {
    const tb = $('#historyTable tbody');
    tb.textContent = '';
    if (!r.ok) return;
    if (!r.data.available) {
      msg('#historyMsg', r.data.error || '取不到历史记录', 'err');
      return;
    }
    msg('#historyMsg', '');
    const rooms = (r.data.data && r.data.data.rooms) || [];
    historyRows = rooms;

    // ID → 邮箱
    const mail = {};
    if (ra && ra.ok && ra.data) {
      for (const a of (ra.data.accounts || [])) mail[a.id] = a.email;
    }
    historyMail = mail;

    const cnt = $('#historyCount');
    if (cnt) cnt.textContent = rooms.length ? '共 ' + rooms.length + ' 个房间' : '';

    if (!rooms.length) {
      msg('#historyMsg', q ? '没有匹配的房间。' : '还没有记录。开一局之后这里就会有。', '');
      return;
    }
    for (const x of rooms) tb.appendChild(historyRow(x, mail));
  });
}

// 房间/在线时长：只到分钟。
// 📌 后台通用那个 fmtDur 会输出"25 分 0 秒"——房间级别精确到秒没意义，
// 反而占地方、还难读 ✓
function fmtSpan(sec) {
  sec = Math.max(0, Math.round(sec || 0));
  const m = Math.round(sec / 60);
  if (m < 1) return '不到 1 分钟';
  if (m < 60) return m + ' 分钟';
  const h = Math.floor(m / 60), r = m % 60;
  return r ? h + ' 小时 ' + r + ' 分' : h + ' 小时';
}

// 成员进出时间：跟房间开始那天同一天就只给 HH:MM，跨天才带月日。
// 📌 一局里每个成员都挂一串"2026-10-17 21:29"太吵，看的人只想比较先后 ✓
function fmtClock(unix, base) {
  if (!unix) return '—';
  const d = new Date(unix * 1000), b = new Date(base * 1000);
  const p = (n) => String(n).padStart(2, '0');
  const hm = p(d.getHours()) + ':' + p(d.getMinutes());
  if (d.toDateString() === b.toDateString()) return hm;
  return (d.getMonth() + 1) + '-' + d.getDate() + ' ' + hm;
}

function historyRow(x, mail) {
  const tr = document.createElement('tr');
  tr.className = 'rowlink';
  // 整行都能点进详情 —— 比只让最后一列那个小链接可点好用得多 ✓
  tr.addEventListener('click', () => { location.hash = '#/history/' + x.code; });

  tr.appendChild(td(x.code));
  tr.appendChild(td(x.game || '—'));
  // 🩸 显示**邮箱**不显示昵称/账号号：昵称随时能改，换个名字就认不出是谁了 ✗
  // 账号 ID 对不上时退成 #号（老记录 / 账号被删）✓
  tr.appendChild(td(acctLabel(x.acct, mail)));
  tr.appendChild(td(fmtTime(x.openedAt)));

  // 🩸 三种情况分开算，**绝不拿"现在"兜底**：
  //   ① closedAt 有      ⇒ 精确时长
  //   ② 还在开（open）    ⇒ 到现在为止，说"已开"
  //   ③ 没有结束时间又没开着 ⇒ 记录在某处断了，只能给"至少"（用到 lastAt）
  // 拿 now 兜底的话，③ 的时长会**每秒都涨**，看着像房间还开着 ✗
  //（老板 2026-10-17 就是看到「35 分钟（未正常结束）」而且一直涨）
  const end = roomEndSec(x, Math.floor(Date.now() / 1000));
  const dur = td(fmtSpan(end - x.openedAt));
  if (x.open) {
    dur.textContent = '已开 ' + fmtSpan(end - x.openedAt);
  } else if (!x.closedAt) {
    dur.textContent = '≥ ' + fmtSpan(end - x.openedAt) + '（记录中断）';
    dur.className = 'hint';
  }
  tr.appendChild(dur);

  tr.appendChild(td(String(x.maxOnline || 0)));
  tr.appendChild(td(String((x.members || []).length) + ' 人次'));

  const go = td('查看 →');
  go.className = 'go';
  tr.appendChild(go);
  return tr;
}

// 历史房间详情：`#/history/TIxxxx`
//
// 📌 数据还是走列表那个接口（`?q=房间号`），**不另开一个后端接口** ——
//    反正一次只查一个号，返回量很小；多一个接口就多一处要维护的东西 ✓
async function loadHistoryDetail(code) {
  code = String(code || '').trim().toUpperCase();
  $('#hdTitle').textContent = '房间 ' + code;
  $('#hdSub').textContent = '';

  const [r, ra] = await Promise.all([
    call('/api/admin/rooms/history?q=' + encodeURIComponent(code)),
    call('/api/admin/accounts'),
  ]);
  if (!r.ok) return;

  const mail = {};
  if (ra && ra.ok && ra.data) {
    for (const a of (ra.data.accounts || [])) mail[a.id] = a.email;
  }

  // 先清空：查不到时屏幕上不能还留着上一个房间的内容 ✗
  $('#hdCards').textContent = '';
  $('#hdSessions').textContent = '';

  if (!r.data.available) {
    msg('#hdMsg', r.data.error || '取不到房间历史', 'err');
    return;
  }
  const all = (r.data.data && r.data.data.rooms) || [];
  // `q` 是**模糊**匹配（搜"TI9"会带出别的号）⇒ 这里只要房间号完全相同的。
  // 同一个号被开过几次就有几条，全都列出来 —— 那才是完整的历史 ✓
  const rows = all.filter((x) => String(x.code).toUpperCase() === code);
  if (!rows.length) {
    msg('#hdMsg', '找不到这个房间的记录。可能已经超过 30 天被清掉了。', 'err');
    return;
  }
  msg('#hdMsg', '');

  // 最近的在前（接口本来就是这顺序，这里再保一次险）
  rows.sort((a, b) => b.openedAt - a.openedAt);
  const first = rows[0];
  const acc = acctLabel(first.acct, mail);

  $('#hdSub').textContent = '游戏 ' + (first.game || '未上报') +
    ' · 房主 ' + acc + ' · 共 ' + rows.length + ' 局';

  const cards = $('#hdCards');
  // 只放两张卡：局数在副标题里已经有了，多一张会把房主邮箱挤成两行 ✗
  cards.appendChild(ovCard({ k: '游戏', v: first.game || '未上报' }));
  cards.appendChild(ovCard({ k: '房主', v: acc }));

  rows.forEach((x, idx) => $('#hdSessions').appendChild(historySession(x, idx, rows.length, mail)));
}

// 这一局算到**哪一刻**为止。
//
// 🩸🩸 绝不能用 `closedAt || now` 兜底：没有结束时间的房间（服务器重启 / 进程被杀，
// close 事件压根没写）会拿"现在"去减开始时间 ⇒ **时长每秒都在涨**，
// 看着像房间还开着 ✗（老板 2026-10-17 截图：「35 分钟（未正常结束）」还一直涨）
//
// 三种情况：
//   · 有结束时间 ⇒ 就是它
//   · 还在开     ⇒ 到现在（这一种用 now 是对的）
//   · 都没有     ⇒ 只算到**最后一条记录**（lastAt），那是我们唯一确知的时刻 ✓
function roomEndSec(x, nowSec) {
  if (x.closedAt) return x.closedAt;
  if (x.open) return nowSec;
  return x.lastAt || x.openedAt;
}

// 账号 ID → 邮箱。对不上就退成 "#号"，别显示空白（空白会让人以为"没登录"）
function acctLabel(id, mail) {
  if (!id) return '—';
  return (mail && mail[id]) || ('#' + id);
}

// 一个表头格子（app.js 里原来没有 th()，这儿补一个）
function th(text) {
  const c = document.createElement('th');
  c.textContent = text;
  return c;
}

// 一局：一个小标题 + 一张成员表
function historySession(x, idx, total, mail) {
  const end = roomEndSec(x, Math.floor(Date.now() / 1000));
  const box = document.createElement('div');
  box.className = 'card';

  const title = document.createElement('h2');
  title.textContent = (total > 1 ? '第 ' + (idx + 1) + ' 局' : '成员进出') +
    '　' + fmtTime(x.openedAt) + ' 开始';
  box.appendChild(title);

  const hint = document.createElement('p');
  hint.className = 'hint';
  if (x.open) {
    hint.textContent = '这一局现在还在开着，已开 ' + fmtSpan(end - x.openedAt) +
      '，最多同时在线 ' + (x.maxOnline || 0) + ' 人。';
  } else if (!x.closedAt) {
    // 说清"记录停在哪"，别让人以为房间还开着 —— 下面是**固定的时刻**，不是现在 ✓
    hint.textContent = '这一局没有结束时间：记录到 ' + fmtTime(x.lastAt || x.openedAt) +
      ' 就断了（服务器重启、或进程被杀），所以下面的时长只能算到那时候。' +
      '有些人的离开时间也就没记到。最多同时在线 ' + (x.maxOnline || 0) + ' 人。';
  } else {
    hint.textContent = '共开了 ' + fmtSpan(x.closedAt - x.openedAt) +
      '，最多同时在线 ' + (x.maxOnline || 0) + ' 人。';
  }
  box.appendChild(hint);

  const table = document.createElement('table');
  const thead = document.createElement('thead');
  const htr = document.createElement('tr');
  for (const h of ['账号', '昵称', '座位', '进入', '离开', '在线时长', '离开原因']) htr.appendChild(th(h));
  thead.appendChild(htr);
  table.appendChild(thead);

  const tbody = document.createElement('tbody');
  const ms = (x.members || []).slice().sort((a, b) => a.joinedAt - b.joinedAt);
  if (!ms.length) {
    const tr = document.createElement('tr');
    const c = td('这一局没记录到成员（可能连上之前就断开了）');
    c.colSpan = 7;
    c.className = 'hint';
    tr.appendChild(c);
    tbody.appendChild(tr);
  }
  for (const m of ms) {
    const tr = document.createElement('tr');
    // 账号在前、昵称在后：身份认账号，昵称只是"他当时叫什么" ✓
    tr.appendChild(td(acctLabel(m.acct, mail)));
    tr.appendChild(td(m.label || ('玩家 ' + (m.seat + 1))));
    tr.appendChild(td(m.host ? '房主' : String(m.seat)));

    // 同一天只给 HH:MM，跨天才带月日 —— 一行里塞满完整日期太吵
    tr.appendChild(td(fmtClock(m.joinedAt, x.openedAt)));
    if (m.leftAt) {
      tr.appendChild(td(fmtClock(m.leftAt, x.openedAt)));
      tr.appendChild(td(fmtSpan(m.leftAt - m.joinedAt)));
      tr.appendChild(td(LEAVE_WHY[m.why] || m.why || '—'));
    } else if (x.open) {
      const c = td('还在房间里');
      c.className = 'hint';
      tr.appendChild(c);
      tr.appendChild(td('—'));
      tr.appendChild(td('—'));
    } else {
      const c = td('没记到');
      c.className = 'hint';
      tr.appendChild(c);
      tr.appendChild(td('—'));
      tr.appendChild(td('—'));
    }
    tbody.appendChild(tr);
  }
  table.appendChild(tbody);
  box.appendChild(table);
  return box;
}

// CSV 导出：**一个成员一行**（房间信息重复），丢进 Excel 就能按人筛
function exportHistoryCSV() {
  if (!historyRows.length) {
    msg('#historyMsg', '没有可导出的记录', 'err');
    return;
  }
  const q = (v) => '"' + String(v === undefined || v === null ? '' : v).replace(/"/g, '""') + '"';
  const head = ['房间ID', '游戏', '房主邮箱', '开始', '结束', '时长(秒)',
    '成员邮箱', '昵称', '座位', '是房主', '进入', '离开', '离开原因'];
  const lines = [head.join(',')];

  for (const x of historyRows) {
    const dur = x.closedAt ? x.closedAt - x.openedAt : '';
    const base = [q(x.code), q(x.game), q(acctLabel(x.acct, historyMail)), q(fmtTime(x.openedAt)),
      q(fmtTime(x.closedAt)), q(dur)];
    const ms = x.members || [];
    if (!ms.length) {
      lines.push(base.concat([q(''), q(''), q(''), q(''), q(''), q(''), q('')]).join(','));
      continue;
    }
    for (const m of ms) {
      lines.push(base.concat([
        q(acctLabel(m.acct, historyMail)), q(m.label), q(m.seat), q(m.host ? '是' : ''),
        q(fmtTime(m.joinedAt)), q(fmtTime(m.leftAt)), q(LEAVE_WHY[m.why] || m.why || ''),
      ]).join(','));
    }
  }

  // BOM：不加的话 Excel 打开中文是乱码 ✗
  const blob = new Blob(['\uFEFF' + lines.join('\n')], { type: 'text/csv;charset=utf-8' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = 'TI联机-历史房间-' + new Date().toISOString().slice(0, 10) + '.csv';
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}

(function () {
  const back = $('#hdBack');
  if (back) back.addEventListener('click', () => { location.hash = '#/history'; });
  const btn = $('#reloadHistory');
  if (btn) btn.addEventListener('click', loadHistory);
  const exp = $('#exportHistory');
  if (exp) exp.addEventListener('click', exportHistoryCSV);
  const inp = $('#historyQ');
  if (inp) {
    // 输入就搜，但压一下频率 —— 每敲一个字就发请求太吵
    let timer = null;
    inp.addEventListener('input', () => {
      clearTimeout(timer);
      timer = setTimeout(loadHistory, 250);
    });
  }
})();
