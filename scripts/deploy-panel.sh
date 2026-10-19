#!/usr/bin/env bash
# 把管理面板部署到 server 上。
#
# 用法：bash scripts/deploy-panel.sh <user@host> <SSH口令>
#   bash scripts/deploy-panel.sh root@<你的服务器IP> '<口令>'
#
# ⚠️ **在项目根目录下跑**（下面的 LOCAL_BIN 是相对路径）
#
# 环境变量：
#   P2P_DATA=/root/p2plink-data   账号库目录（**要先建好管理员**，见下面）
#   P2P_SIGNAL=/root/p2plink-data/local.sock  信令侧那个本机 **Unix socket**（取房间指标、关房间都走它）
#   P2P_PORT=8443                 面板自己监听的端口（**只监听 127.0.0.1**）
#
# 发信（登录验证码用）—— 🩸 **口令别写在这个脚本里、也别进 git**：
#   在服务器上建一个只有 root 能读的文件：
#     install -m 600 /dev/null /root/p2plink-smtp.env
#     cat > /root/p2plink-smtp.env <<'EOF'
#     P2P_SMTP_HOST=smtp.qq.com
#     P2P_SMTP_PORT=465
#     P2P_SMTP_USER=xxxxx@qq.com
#     P2P_SMTP_PASS=授权码
#     EOF
#   systemd 单元会把它读进来（见下面 5/6）。**没这个文件也能起**，只是验证码登录不可用 ✓
#
# 先本地交叉编译：
#   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/panel-linux-amd64 ./cmd/panel
#
# 🩸 部署顺序有讲究：**先建账号库和管理员，再起面板**。
# 反过来做的话，面板第一次启动会在空目录里造一套新库 ——
# 而那时候你会以为"账号还在"，直到有人登不上才发现 ✗
#
# 这台机器上还跑着别的东西（nginx / postgres / blog）——
# 脚本**只碰**：
#   · $REMOTE_BIN（默认 /root/p2plink-panel）
#   · /etc/systemd/system/p2plink-panel.service
# nginx 那边要**另外**加一个 server block（见 scripts/nginx-panel.conf），
# 脚本不动它 ✓
set -euo pipefail

export PATH="/c/msys64/mingw64/bin:/usr/bin:/bin:/c/Windows/System32:$PATH"

if [ $# -lt 2 ]; then
  echo "用法: bash scripts/deploy-panel.sh <user@host> <SSH口令>"
  exit 1
fi

TARGET="$1"
PASS="$2"
SERVICE=p2plink-panel
REMOTE_BIN="/root/p2plink-panel"
DATA_DIR="${P2P_DATA:-/root/p2plink-data}"
# 🩸 这里是个 **Unix socket 路径**，不是 URL（原来写的是 http://127.0.0.1:8081）。
# 换 socket 的原因：回环地址挡不住**同机上的其它进程**，而"从后台关房间"是
# 能踢人的写接口 —— 那台机器上还跑着 nginx / postgres / 博客 ✗
# 权限交给文件系统判：那个 socket 是 0600，只有 root（也就是面板和信令）连得上 ✓
SIGNAL="${P2P_SIGNAL:-$DATA_DIR/local.sock}"
PORT="${P2P_PORT:-8443}"
LOCAL_BIN=dist/panel-linux-amd64

if [ ! -f "$LOCAL_BIN" ]; then
  echo "找不到 $LOCAL_BIN —— 先交叉编译：
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $LOCAL_BIN ./cmd/panel"
  exit 1
fi

# 🩸 上传前先验**它真的是 Linux 二进制**。
#
# 2026-10-16 踩的：交叉编译时忘了带 GOOS=linux GOARCH=amd64
# ⇒ 传上去的是个 Windows 的 exe ⇒ 线上报
#   `cannot execute binary file: Exec format error`
# 而那一刻服务是**停着的**（脚本先停再传）—— 停服时间白白拉长 ✗
#
# Windows 的 Git Bash 里没有 `file` 命令，所以直接看开头 4 个字节：
#   ELF 是 7f 45 4c 46（\x7fELF）；Windows 的 exe 是 4d 5a（MZ）
MAGIC=$(head -c 4 "$LOCAL_BIN" | od -An -tx1 | tr -d ' \n')
if [ "$MAGIC" != "7f454c46" ]; then
  echo "✗ $LOCAL_BIN 不是 Linux 二进制（开头是 $MAGIC）" >&2
  echo "  多半是编译时漏了 GOOS/GOARCH。重新编：" >&2
  echo "    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $LOCAL_BIN <那个包>" >&2
  exit 1
fi
echo "   二进制格式：ELF ✓"

ASK=$(mktemp -t askpass.XXXXXX)
cleanup() { rm -f "$ASK"; }
trap cleanup EXIT
printf '#!/bin/sh\necho %s\n' "$PASS" > "$ASK"
chmod +x "$ASK"
export SSH_ASKPASS="$ASK" SSH_ASKPASS_REQUIRE=force DISPLAY=:0

SSH_OPTS=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null
          -o ConnectTimeout=20 -o LogLevel=ERROR)
ssh_run() { ssh "${SSH_OPTS[@]}" "$TARGET" "$@"; }

# 中途挂了别把服务留在"停着"的状态
rescue() {
  rc=$?
  trap - EXIT
  if [ $rc -ne 0 ]; then
    echo
    echo "!! 部署失败（退出码 $rc），抢救中…"
    ssh_run "systemctl is-active $SERVICE >/dev/null 2>&1 || { \
               cp -f '${REMOTE_BIN}.prev' '$REMOTE_BIN' 2>/dev/null || true; \
               systemctl start $SERVICE 2>/dev/null || true; \
             }; echo 当前状态: \$(systemctl is-active $SERVICE 2>/dev/null || echo 无此服务)" || true
  fi
  exit $rc
}
trap rescue EXIT

echo "== 1/6 连上并看现状 =="
ssh_run "uname -srm"
if ssh_run "systemctl list-unit-files | grep -q '^$SERVICE'"; then
  echo "   已有服务，当前状态：$(ssh_run "systemctl is-active $SERVICE")"
  OLD_BIN=$(ssh_run "systemctl show -p ExecStart --value $SERVICE | sed -n 's/.*path=\\([^ ;]*\\).*/\\1/p'")
  [ -n "$OLD_BIN" ] && REMOTE_BIN="$OLD_BIN"
  echo "   远程二进制：$REMOTE_BIN"
else
  echo "   这台机器上还没有 $SERVICE，等下新建"
fi
echo "   本地新包：$LOCAL_BIN ($(stat -c %s "$LOCAL_BIN") 字节)"

echo "== 2/6 查账号库（没有就别往下走了）=="
if ! ssh_run "[ -f '$DATA_DIR/accounts.json' ]"; then
  echo "   ✗ $DATA_DIR/accounts.json 不存在。" >&2
  echo "     先在那台机器上建账号库和管理员："
  echo "       $REMOTE_BIN admin-add -data $DATA_DIR -email 你的邮箱@qq.com"
  echo "     （**别在这之前起面板** —— 它会造一套空库，而你以为账号还在 ✗）" >&2
  exit 1
fi
echo "   账号库在：$DATA_DIR（$(ssh_run "wc -c < '$DATA_DIR/accounts.json'") 字节）"

echo "== 3/6 停服务（覆盖正在运行的二进制会失败）=="
ssh_run "systemctl stop $SERVICE 2>/dev/null || true"

echo "== 4/6 备份 + 上传 =="
ssh_run "if [ -f '$REMOTE_BIN' ]; then cp -f '$REMOTE_BIN' '${REMOTE_BIN}.prev' && echo '   已备份'; else echo '   （没有旧版，首次部署）'; fi"
scp "${SSH_OPTS[@]}" "$LOCAL_BIN" "$TARGET:$REMOTE_BIN"
ssh_run "chmod +x '$REMOTE_BIN'"

echo "== 5/6 写 systemd 单元 =="
# ⚠️ **不带任何 TLS 参数**：TLS 由前置的 nginx 终结（见 scripts/nginx-panel.conf），
# 面板自己只监听 127.0.0.1 —— 这样它**不可能**被从公网直接够到 ✓
ssh_run "cat > /etc/systemd/system/$SERVICE.service <<'UNIT'
[Unit]
Description=P2PLink Admin Panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
# 🩸 发信口令走这个文件（0600，只有 root 能读）—— **不进 git、也不进命令行** ✗
# 前面那个减号是"文件不存在也别起不来"：没配发信时验证码登录就是关着的 ✓
EnvironmentFile=-/root/p2plink-smtp.env
ExecStart=$REMOTE_BIN -listen 127.0.0.1:$PORT -data $DATA_DIR -signal $SIGNAL
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload && systemctl enable $SERVICE >/dev/null 2>&1 && echo '   单元已就位'"

echo "== 6/6 启动 + 验证 =="
ssh_run "systemctl restart $SERVICE"
sleep 1.5
ssh_run "systemctl is-active $SERVICE; echo '---- 最近日志 ----'; journalctl -u $SERVICE -n 8 --no-pager | tail -8"

echo
echo "本地验一把（在你自己电脑上跑）："
echo "  curl -sS -o /dev/null -w '%{http_code}\\n' https://panel.tinyaii.cn/admin/"
echo "  # 要看到 200；503/502 = nginx 连不上面板，404 = nginx 没有这个域名的 server block"
echo
echo "回滚办法："
echo "  bash scripts/sshx.sh $TARGET '<口令>' \"systemctl stop $SERVICE && cp -f ${REMOTE_BIN}.prev $REMOTE_BIN && systemctl start $SERVICE\""
