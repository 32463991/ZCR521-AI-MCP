#!/bin/sh
set -eu

port="${1:-5322}"
case "$port" in
  *[!0-9]*|"") echo "端口必须是 1..65535 的整数" >&2; exit 2 ;;
esac
if [ "$port" -lt 1 ] || [ "$port" -gt 65535 ]; then
  echo "端口必须是 1..65535 的整数" >&2
  exit 2
fi
if ! command -v adb >/dev/null 2>&1; then
  echo "未找到 adb。请安装 Android Platform Tools，并把 adb 加入 PATH。" >&2
  exit 1
fi
if [ "$(adb get-state 2>/dev/null || true)" != "device" ]; then
  echo "未检测到可用的已授权 Android 设备。" >&2
  exit 1
fi

adb forward "tcp:${port}" "tcp:${port}"
printf '%s\n' \
  "ZCR521 USB 转发已就绪。" \
  "MCP 地址: http://127.0.0.1:${port}/mcp" \
  "仅支持 STDIO 的客户端请启动: zcr521-bridge --url http://127.0.0.1:${port}/mcp"
