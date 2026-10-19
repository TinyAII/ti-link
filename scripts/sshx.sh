#!/usr/bin/env bash
# 带口令跑一条远程命令。
#
#   bash scripts/sshx.sh <user@host> <口令> <远程命令...>
#
# 口令只写进一个临时文件（SSH_ASKPASS 机制要求），退出时删掉 —— 不留档。
set -euo pipefail

export PATH="/c/msys64/mingw64/bin:/usr/bin:/bin:/c/Windows/System32:$PATH"

if [ $# -lt 3 ]; then
  echo "用法: bash scripts/sshx.sh <user@host> <口令> <远程命令...>"
  exit 1
fi

HOST="$1"; PASS="$2"; shift 2

ASK=$(mktemp -t askpass.XXXXXX)
cleanup() { rm -f "$ASK"; }
trap cleanup EXIT

printf '#!/bin/sh\necho %s\n' "$PASS" > "$ASK"
chmod +x "$ASK"
export SSH_ASKPASS="$ASK" SSH_ASKPASS_REQUIRE=force DISPLAY=:0

exec ssh \
  -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile=/dev/null \
  -o ConnectTimeout=20 \
  -o LogLevel=ERROR \
  "$HOST" "$@"
