/* ---------- 个人信息（昵称 + 头像） ---------- */
// 昵称只存在本机设置里，建房/加房时随注册报给服务器。
// 现在**只有个人信息页**能改它 —— 原来联机页那两个输入框已经挪走了。
let myNick = '';
// 头像存的是**下标**（一个数字），图库由 Go 侧内联进来。
// 协议里跑的就是这个数字 —— 传图本身太占带宽，图库两端各内置一份就够了。
let myAvatarIdx = 0;
const AVATARS = __AVATARS__;   // [{g:"分类", src:"data:image/png;base64,..."}, ...]

// 名字首字头像的底色：按名字做个稳定 hash —— 同一个人到哪都是同一个色。
// 只有在「没有内置头像可用」时才会看到这个（兜底用）。
const AVATAR_COLORS = ['#4f46e5', '#0d9488', '#b45309', '#be185d', '#0369a1', '#7c3aed'];
function avatarOf(text, fallback) {
  const s = (text || '').trim();
  const src = s || fallback || '?';
  let h = 0;
  for (const ch of src) h = (h * 31 + ch.codePointAt(0)) >>> 0;
  return { ch: Array.from(src)[0], color: AVATAR_COLORS[h % AVATAR_COLORS.length] };
}

// 画一个头像。能查到内置图就用图；查不到就退回用名字首字画一个。
//
// 兜底那条别删：下标越界的情形真会发生 —— 对方版本比我们新（挑了张我们图库里
// 还没有的头像）、或者对方是老版本（根本没发这个字段）。没兜底就是一片空白。
function applyAvatar(el, idx, text, fallback) {
  if (!el) return;
  const im = AVATARS[idx];
  if (im && im.src) {
    el.textContent = '';
    el.style.backgroundColor = 'transparent';
    el.style.backgroundImage = 'url("' + im.src + '")';
    el.style.backgroundSize = 'cover';
    el.style.backgroundPosition = 'center';
    return;
  }
  el.style.backgroundImage = '';
  el.style.backgroundSize = '';
  const a = avatarOf(text, fallback);
  el.textContent = a.ch;
  el.style.backgroundColor = a.color;
  el.style.color = '#fff';
}

/* ---------- 默认昵称：随机拼一个 ---------- */
// 结构：{态度}{做什么}的{身份}{名字} →「喜欢」+「捣蛋」+「的」+「雇员」+「沐恩」
//                                        = 喜欢捣蛋的雇员沐恩
//
// 📌 词表**不在这儿** —— 由 Go 侧注入（internal/nick 是唯一来源，一份词库两处用：
// 这儿摇名字，穿透服务器那边校验"是不是词库能拼出来的"）。
// ⚠️ 别在这儿手写词表：两份迟早分叉，而分叉的表现是"客户端摇得出、服务器不收"，
// 用户只看到自己进不去房间 ✗
// 改词请去 internal/nick/nick.go —— 那儿有测试盯着词数、重复和长度上限。
const NICK_WORDS = __NICK_WORDS__;

// 摇一个昵称。
// 末尾那个兜底是**保险**不是常规路径：按现在的词库长度最长只会到 11 字，
// 到不了 12。万一以后有人往词库里塞了长词，宁可重摇也不要截半个词出来。
function rollNick(tries) {
  const pick = a => a[Math.floor(Math.random() * a.length)];
  for (let i = 0; i < (tries || 8); i++) {
    const s = pick(NICK_WORDS.mood) + pick(NICK_WORDS.deed) + '的' +
      pick(NICK_WORDS.role) + pick(NICK_WORDS.name);
    if (Array.from(s).length <= 12) return s;
  }
  return '喜欢捣蛋的雇员沐恩';
}

// 把「我是谁」刷到所有显示它的地方：个人信息页、建房 / 加房页的身份提示
function applyMe() {
  // 昵称那格现在是**只读的字**（不再是输入框）—— 名字只能从词库里摇出来。
  const el = $('profNick');
  if (el) {
    el.textContent = myNick || '（还没起名）';
    el.classList.toggle('unset', !myNick);
  }
  applyAvatar($('profAvatar'), myAvatarIdx, myNick, '我');
  ['Create', 'Join'].forEach(sfx => {
    const nm = $('ident' + sfx + 'Name');
    if (nm) {
      nm.textContent = myNick || '未设置昵称（会显示成「玩家 N」）';
      nm.classList.toggle('unset', !myNick);
    }
    applyAvatar($('ident' + sfx + 'Av'), myAvatarIdx, myNick, '我');
  });
  markPickedAvatar();
}

function applyNick(n) {
  myNick = (n || '').trim();
  applyMe();
}

function applyAvatarIndex(i) {
  myAvatarIdx = (typeof i === 'number' && i >= 0) ? i : 0;
  applyMe();
}

/* ---------- 头像选择器（个人信息页） ---------- */
// 分类名和图库顺序都由 Go 侧给（AVATARS 每项带 g），页面不写死下标范围 ——
// 图库以后加图不用回来改这里。
function buildAvatarPicker() {
  const tabs = $('avTabs'), grids = $('avGrids');
  if (!tabs || !grids) return;
  const groups = [];
  AVATARS.forEach((a, i) => {
    const name = (a && a.g) || '其它';
    let g = groups.find(x => x.name === name);
    if (!g) { g = { name: name, items: [] }; groups.push(g); }
    g.items.push(i);
  });

  tabs.innerHTML = '';
  grids.innerHTML = '';
  groups.forEach((g, gi) => {
    const t = document.createElement('button');
    t.type = 'button';
    t.className = 'avtab' + (gi === 0 ? ' active' : '');
    t.textContent = g.name;
    t.onclick = () => {
      tabs.querySelectorAll('.avtab').forEach(x => x.classList.toggle('active', x === t));
      grids.querySelectorAll('.avpane').forEach(p =>
        p.classList.toggle('active', p.dataset.pane === String(gi)));
    };
    tabs.appendChild(t);

    const pane = document.createElement('div');
    pane.className = 'avpane' + (gi === 0 ? ' active' : '');
    pane.dataset.pane = String(gi);
    g.items.forEach(i => {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'avopt';
      b.dataset.av = String(i);
      b.title = '头像 ' + i;
      b.onclick = () => pickAvatar(i);
      const im = AVATARS[i];
      if (im && im.src) {
        b.style.backgroundImage = 'url("' + im.src + '")';
        b.style.backgroundSize = 'cover';
      }
      pane.appendChild(b);
    });
    grids.appendChild(pane);
  });
  markPickedAvatar();
}

function markPickedAvatar() {
  const grids = $('avGrids');
  if (!grids) return;
  grids.querySelectorAll('.avopt').forEach(b => {
    b.classList.toggle('on', Number(b.dataset.av) === myAvatarIdx);
  });
}

// 点一张就换 —— 立即存下来。头像不是身份，随手换没负担。
async function pickAvatar(i) {
  const prev = myAvatarIdx;
  applyAvatarIndex(i);
  const err = await window.SetSetting('avatar', String(i));
  if (err) { applyAvatarIndex(prev); setStatus(err, 'err'); return; }
  setStatus('头像已换', 'ok');
}

