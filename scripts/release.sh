#!/usr/bin/env bash
# 一条命令发版：编客户端 → 打包 → 签更新源 → 打上传清单
#
#   bash scripts/release.sh 1.0.1 "改了什么"
#
# 产出（都在 dist/）：
#   p2plink-gui.exe / p2plink.exe  ← 客户端
#   TI联机/ + TI联机.zip            ← 发朋友的包（复用 build-client.sh）
#   latest.json                     ← **带签名**的更新源
#
# 🩸🩸 为什么要有这条（2026-10-17）：
#   更新源现在**必须带签名** —— 客户端验签，没过就当"这次没查成" ✗
#   而签名私钥只在老板手上、签名这步原来纯靠人记 ⇒ **迟早忘** ✗
#   （这就是老板那句"这是我的工作吗"指的：不该靠记性 ✓）
#   ⇒ 把它变成脚本的一步，忘不了 ✓
#
# 📌 只做到"产物齐了 + 清单打出来"，**不自动上传** ✗
#   上传是发出去的动作，由老板说了算 ✓
set -euo pipefail

export PATH="/c/msys64/mingw64/bin:/usr/bin:/bin:/c/Windows/System32:$PATH"
cd "$(dirname "$0")/.."

V="${1:-}"
NOTE="${2:-}"
if [ -z "$V" ]; then
  echo "用法: bash scripts/release.sh <版本号> [一句话说明]" >&2
  echo "  例: bash scripts/release.sh 1.0.1 \"修了房间密码的提示\"" >&2
  exit 1
fi

# ── ① 版本号必须先在代码里改好 ──────────────────────────
# 🩸 不自动改：改常量属于"改行为"，得老板自己拍板 ✓
# 而且两个号不一致的话，客户端会**自称旧版本号** ⇒ 更新提示永远是错的 ✗
APPVER=$(grep -oE 'appVersion = "[^"]+"' cmd/gui/settings.go | sed -E 's/.*"([^"]+)"/\1/')
if [ "$APPVER" != "$V" ]; then
  echo "✗ 版本号对不上：" >&2
  echo "   cmd/gui/settings.go 里 appVersion = ${APPVER:-（没读到）}" >&2
  echo "   这次要发的是 $V" >&2
  echo "   ⇒ 先把 settings.go 那行改成 \"$V\"，再跑一次（别让脚本替你改代码 ✗）" >&2
  exit 1
fi

# ── ② 私钥必须在 ────────────────────────────────────────
# 🩸 静默产出一个**没签名**的 latest.json 是最糟的结果（用户端一律"查不到更新"）✗
KEY="dist/update-signing-key.txt"
if [ ! -f "$KEY" ] && [ -z "${P2PLINK_SIGN_KEY:-}" ]; then
  echo "✗ 找不到签名私钥（$KEY 不在，环境变量 P2PLINK_SIGN_KEY 也没给）" >&2
  echo "  它是本地文件、不进 git。找回来再发版。" >&2
  exit 1
fi

# ── ③ 编 + 打包（复用 build-client.sh，别重复造）─────────
bash scripts/build-client.sh

# ── ④ 签更新源 ──────────────────────────────────────────
echo
echo "== 签更新源 =="
go run ./tool/signlatest -version "$V" -note "$NOTE" > dist/latest.json
grep -q '"sig"' dist/latest.json || {
  echo "✗ latest.json 里没有 sig —— 签名没成" >&2
  exit 1
}

# ── ⑤ 用**客户端内置的公钥**验一遍 ──────────────────────
# 🩸 必须用 update.go 里那把，不能用私钥文件里记的那把 ——
#    后者是自己登记自己，验过了也说明不了客户端会认 ✗
PUB=$(grep -oE 'var updatePubKey = "[^"]+"' cmd/gui/update.go | sed -E 's/.*"([^"]+)"/\1/')
if [ -z "$PUB" ]; then
  echo "✗ 没从 cmd/gui/update.go 里读出 updatePubKey" >&2
  exit 1
fi
go run ./tool/signlatest -verify dist/latest.json -pub "$PUB"

# ── ⑥ 上传清单 ──────────────────────────────────────────
echo
echo "== 产物 =="
ls -la dist/latest.json dist/TI联机.zip dist/p2plink-gui.exe dist/p2plink.exe \
  | awk '{printf "   %-26s %10d 字节\n", $NF, $5}'
echo "   版本 $V；说明：${NOTE:-（空）}"
echo
echo "== 上传清单（脚本不自动传，你说了算）=="
cat <<'EOF'
   ① dist/latest.json                    → 站点根目录（更新源，客户端拉的就是它）
   ② dist/TI联机-安装包-<版本>.exe       → 夸克网盘（推荐大家下这个）
   ③ dist/TI联机.zip                     → 夸克网盘（绿色版）
   ④ site/index.html + site/download.html + site/shots/  → pages 仓库

   📌 命令行版（dist/p2plink.exe）**不对外发**（2026-10-17 定）：
      它没有登录功能，新用户跑起来必然"请先登录"✗ 编译产物留着自己调试 ✓
   📌 下载页里写死了版本号/文件名/大小/SHA256 ——
      **重新编过包就要回去改 site/download.html**，否则给出的校验值是错的 ✗

   ⚠️ latest.json 是**验证过签名的**（用客户端内置公钥验过）—— 别手改它 ✗
   改了任何一个字段，签名立刻失效 ⇒ 所有客户端都查不到更新 ✗
EOF
echo
echo "好了。上传完抽查一下：curl -sS https://ti.tinyaii.cn/latest.json | head -6"
