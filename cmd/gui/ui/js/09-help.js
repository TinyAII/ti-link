/* ---------- 帮助页：分类切换 ---------- */
// 点一个分类看一类。原来七八条问题平铺在一页上，用户得从头扫到尾才能找到
// 自己那条 ✗
//
// 分类按钮和内容面板靠 data-cat / data-panel 对上，**不写死顺序** ——
// 以后加分类只要在 HTML 里各加一个，这个文件不用动 ✓

(function () {
  const tabs = document.querySelectorAll('#helpCats .cat');
  if (!tabs.length) return;

  function pick(name) {
    tabs.forEach(b => b.classList.toggle('active', b.dataset.cat === name));
    document.querySelectorAll('#page-help .catpanel').forEach(p => {
      p.classList.toggle('show', p.dataset.panel === name);
    });
  }

  tabs.forEach(b => { b.onclick = () => pick(b.dataset.cat); });
})();

/* ---------- 帮助页：挑游戏看教程 ---------- */
// 帮助页**不跟着**"当前在玩的游戏"，它自己有一个下拉 ——
// 还没选游戏时也能看，或者想先看看别的游戏怎么弄 ✓
// （帮助页里哪块内容跟着这个下拉变，见 01-core.js 的 renderGameTexts）

(function () {
  const sel = $('helpGameSel');
  if (!sel) return;

  // 选项从 GAMES 来，**不手写一份** —— 手写迟早跟游戏列表分叉 ✗
  GAMES.forEach(g => {
    const o = document.createElement('option');
    o.value = g.name;
    o.textContent = g.name;
    sel.appendChild(o);
  });

  sel.onchange = () => {
    helpGame = sel.value;
    renderGameTexts();
  };
})();
