#!/usr/bin/env bash
# 编客户端那套产物，并把"发朋友的包"一起更新。
#
#   bash scripts/build-client.sh
#
# 产出：
#   dist/p2plink-gui.exe    客户端（界面）
#   dist/p2plink.exe        命令行版
#   dist/TI联机/            发朋友的目录（上面两个 + 使用说明.txt）
#   dist/TI联机.zip         平铺的压缩包（zip 根目录直接是那三个文件）
#
# 🩸 为什么要有个脚本：2026-10-16 出了这么一件事 ——
#    给客户端加了登录页，代码测了、界面截图了、线上也部署了，
#    但**没人重新编 exe**。老板拿旧 exe 一跑："怎么没显示登录页？"
#    根因不是"忘了"，是**编译命令一直靠人肉拼**：
#    GUI 要 CGO + mingw + -H=windowsgui、CLI 不要那个头、打包还要平铺 —— 
#    一步漏掉就出一件坏产物，而且**测不出来**（源码是好的）✗
#    ⇒ 把它收成一条命令 ✓
set -euo pipefail

export PATH="/c/msys64/mingw64/bin:/usr/bin:/bin:/c/Windows/System32:$PATH"

cd "$(dirname "$0")/.."

# 没这个 PATH，GUI 会报 `C compiler "gcc" not found`
case ":$PATH:" in
  *msys64/mingw64*) ;;
  *) echo "✗ PATH 里没有 mingw64，GUI 编不了" >&2; exit 1 ;;
esac

echo "== 0/6 图标资源（app.ico → rsrc.syso）=="
# 🩸 和上面那段是**同一类病**：`.syso` 不会自己跟着 `.ico` 变。
#    换了图标、忘了 windres ⇒ 编出来的 exe 还是旧图标，
#    而 ico / rc / 源码 看哪儿都对 ✗ ⇒ 干脆每次重编，反正不到 1 秒。
if [ -f cmd/gui/app.rc ] && [ -f cmd/gui/app.ico ]; then
  windres -i cmd/gui/app.rc -o cmd/gui/rsrc.syso -O coff
  echo "   rsrc.syso $(stat -c %s cmd/gui/rsrc.syso) 字节 ✓"
else
  echo "   跳过（没有 app.rc / app.ico，exe 不带图标）"
fi

echo "== 1/6 编 GUI（-H=windowsgui：不带这个会弹个黑框）=="
CGO_ENABLED=1 go build -ldflags "-H=windowsgui -s -w" -o dist/p2plink-gui.exe ./cmd/gui

echo "== 2/6 编 CLI（**不要** -H=windowsgui，它要控制台输出）=="
go build -ldflags "-s -w" -o dist/p2plink.exe ./cmd/node

echo "== 3/6 更新发朋友的包 =="
# 🩸 发出去的东西用**中文名**（老板 2026-10-17 定的）：
#    用户看到的是「TI联机.exe」，不是「p2plink-gui.exe」✗
#    编译产物本身仍叫 p2plink-gui.exe / p2plink.exe —— 脚本和文档都按 ASCII 名引用，
#    **只在打包这一层改名**，两边都不别扭 ✓
# 连旧的 使用说明.txt 一起清掉 —— 这个目录里**只该有脚本放进去的东西**，
# 否则上次残留的文件会悄悄跟着发出去 ✗
rm -f "dist/TI联机/p2plink-gui.exe" "dist/TI联机/p2plink.exe"       "dist/TI联机/使用说明.txt" "dist/TI联机/TI联机-命令行.exe"
cp -f dist/p2plink-gui.exe "dist/TI联机/TI联机.exe"
# 📌 命令行版（dist/p2plink.exe）**只编不发**（老板 2026-10-17）：
#    它没有登录功能，只能读本机那个 token 文件 ⇒ 新用户跑起来必然
#    "请先在客户端里登录再联机"，等于给玩家一个跑不起来的 exe ✗
#    留着编是因为自己调试、或者没有图形界面的机器上还要用它 ✓
python - <<'PY'
import zipfile, pathlib
src = pathlib.Path('dist/TI联机')
out = pathlib.Path('dist/TI联机.zip')
# 📌 不再带 使用说明.txt（老板 2026-10-17：不需要）——
#    内容已经存到 docs/使用说明.txt，不进分发包 ✓
# 📌 也不再带命令行版（见上面那段说明）
names = ['TI联机.exe']
for n in names:
    if not (src / n).exists():
        raise SystemExit('缺文件：%s' % (src / n))
with zipfile.ZipFile(out, 'w', zipfile.ZIP_DEFLATED, compresslevel=6) as z:
    for n in names:
        z.write(src / n, n)      # 第二个参数是归档里的路径 ⇒ 平铺，不带外层文件夹
print('   %s（%.1f MB）' % (out, out.stat().st_size / 1048576))
PY

echo "== 4/6 打安装包（Inno Setup）=="
# 🩸 版本号**只认一个来源**：cmd/gui/settings.go 里的 appVersion。
#    在 .iss 里再写一份的话，迟早出现"exe 是 1.0.1、安装包却写着 1.0.0" ✗
APPVER=$(grep -m1 'const appVersion = ' cmd/gui/settings.go | sed 's/.*"\(.*\)".*/\1/')
if [ -z "$APPVER" ]; then
  echo "✗ 从 cmd/gui/settings.go 里抠不出 appVersion" >&2
  exit 1
fi
echo "   版本 $APPVER"

ISCC=""
for p in "/c/Program Files (x86)/Inno Setup 6/ISCC.exe" \
         "/c/Program Files/Inno Setup 6/ISCC.exe" \
         "$HOME/AppData/Local/Programs/Inno Setup 6/ISCC.exe"; do
  [ -f "$p" ] && { ISCC="$p"; break; }
done

if [ -z "$ISCC" ] || [ "${SKIP_INSTALLER:-0}" = "1" ]; then
  if [ -z "$ISCC" ]; then
    echo "   ✗ 没找到 Inno Setup 的 ISCC.exe —— 安装包打不出来" >&2
    echo "     装一次就好：  winget install --id JRSoftware.InnoSetup -e" >&2
  fi
  if [ "${SKIP_INSTALLER:-0}" = "1" ]; then
    echo "   已按 SKIP_INSTALLER=1 跳过（这一趟没有安装包）"
    # 🩸 跳过不等于"dist 里没有安装包" —— 上一轮的还躺在那儿，
    #    而里面的 exe 是旧的 ⇒ 直接发出去就是发错版本 ✗
    OLD=$(ls dist/TI联机-安装包-*.exe 2>/dev/null | head -3)
    if [ -n "$OLD" ]; then
      echo "   ⚠️ 但 dist 里还留着旧安装包，**里面的客户端不是刚编的**，别发：" >&2
      echo "$OLD" | sed 's/^/        /' >&2
    fi
  else
    echo "   只要 exe 的话：  SKIP_INSTALLER=1 bash scripts/build-client.sh" >&2
    exit 1
  fi
else
  # 旧版本的安装包别留着 —— 发的时候很容易点错，把上一版发出去
  rm -f dist/TI联机-安装包-*.exe
  # 🩸 Git Bash 会把 /DAppVersion=… 当成盘符路径（→ D:\…）⇒ 必须关掉这层参数转换，
  #    否则 ISCC 收到的是一堆文件参数，报 "more than one script filename" ✗
  if ! MSYS2_ARG_CONV_EXCL='*' "$ISCC" "/DAppVersion=$APPVER" scripts/inno/p2plink.iss \
       > tmp/iscc.log 2>&1; then
    tail -25 tmp/iscc.log >&2
    exit 1
  fi
  INST="dist/TI联机-安装包-$APPVER.exe"
  if [ ! -f "$INST" ]; then
    echo "   ✗ ISCC 报成功，但产物不在：$INST" >&2
    exit 1
  fi
  echo "   $INST（$(stat -c %s "$INST") 字节）✓"
fi

echo "== 5/6 自检 =="
# 🩸 这一条是这次的教训：**产物不能比源码旧**。
# 源码改了却忘了重编，产物是旧的那一版 —— 而源码、测试、截图全是好的，
# 看哪儿都"没问题"，只有用户手里那个 exe 是旧的 ✗
NEWEST_SRC=$(find cmd/gui cmd/node internal -name '*.go' -o -name '*.js' -o -name '*.html' -o -name '*.css' 2>/dev/null \
             | xargs ls -t 2>/dev/null | head -1)
if [ -n "$NEWEST_SRC" ] && [ "$NEWEST_SRC" -nt dist/p2plink-gui.exe ]; then
  echo "✗ 有源码比刚编出来的 exe 还新（$NEWEST_SRC）—— 不该发生" >&2
  exit 1
fi
for k in page-login data-logged-in; do
  if grep -qc "$k" dist/p2plink-gui.exe 2>/dev/null; then
    echo "   GUI 里有 $k ✓"
  else
    echo "   ✗ GUI 里没有 $k —— 界面没编进去？" >&2
    exit 1
  fi
done
ls -la dist/p2plink-gui.exe dist/p2plink.exe dist/TI联机.zip | awk '{printf "   %-28s %10d 字节\n", $NF, $5}'
# 安装包如果生成了，也得比 exe 新；否则说明它是上一轮留下的旧包 ✗
if [ -n "${INST:-}" ] && [ -f "$INST" ]; then
  if [ dist/p2plink-gui.exe -nt "$INST" ]; then
    echo "   ✗ 安装包比 exe 还旧（$INST）—— 里面装的是上一版客户端" >&2
    exit 1
  fi
  echo "   安装包比 exe 新 ✓"
fi

echo "== 6/6 资源表 =="
# 图标到底有没有进 exe —— 不看目录里有没有 .ico（那只能证明文件在），
# 直接读 PE 可选头的**数据目录第 3 项**（IMAGE_DIRECTORY_ENTRY_RESOURCE）✓
python - <<'PYRES'
import struct, sys
d = open('dist/p2plink-gui.exe', 'rb').read()
pe = struct.unpack_from('<I', d, 0x3c)[0]
magic = struct.unpack_from('<H', d, pe + 24)[0]
ddoff = pe + 24 + (112 if magic == 0x20b else 96)
size = struct.unpack_from('<I', d, ddoff + 16 + 4)[0]
print('   资源表 %d 字节 %s' % (size, '含图标 ✓' if size > 0 else '✗ 无资源 —— 图标没链进去'))
sys.exit(0 if size > 0 else 1)
PYRES

echo
echo "好了。改完客户端代码就跑这一条，别手拼命令 ✓"
echo "  发人的两个选择："
echo "    装起来用   →  ${INST:-（这一趟没打）}"
echo "    解压即用   →  dist/TI联机/ （或 dist/TI联机.zip）"
