#!/system/bin/sh

SKIPUNZIP=0

zcr_ui() {
  if command -v ui_print >/dev/null 2>&1; then
    ui_print "$1"
  else
    printf '%s\n' "$1"
  fi
}

zcr_abort() {
  if command -v abort >/dev/null 2>&1; then
    abort "$1"
  fi
  zcr_ui "安装失败：$1"
  exit 1
}

[ "${BOOTMODE:-false}" = "true" ] || zcr_abort "仅支持在 Magisk、KernelSU 或 APatch 管理器中安装"
[ -n "${MODPATH:-}" ] || zcr_abort "Root 管理器未提供 MODPATH"
case "$MODPATH" in
  /data/adb/*)
    ;;
  *)
    zcr_abort "异常的模块目录：$MODPATH"
    ;;
esac

ZCR_MODDIR="$MODPATH"
. "$MODPATH/common.sh"

zcr_framework="$(zcr_detect_framework)"
zcr_api="${API:-$(getprop ro.build.version.sdk 2>/dev/null)}"
case "$zcr_api" in
  ''|*[!0-9]*)
    zcr_abort "无法识别 Android API Level"
    ;;
esac
[ "$zcr_api" -ge 26 ] && [ "$zcr_api" -le 36 ] ||
  zcr_abort "仅支持 Android API 26-36，当前 API=$zcr_api"

zcr_arch_source="${ARCH:-$(uname -m 2>/dev/null)}"
case "$zcr_arch_source" in
  arm64|arm64-v8a|aarch64)
    zcr_abi="arm64-v8a"
    ;;
  arm|armeabi-v7a|armv7l|armv8l)
    zcr_abi="armeabi-v7a"
    ;;
  x64|x86_64|amd64)
    zcr_abi="x86_64"
    ;;
  *)
    zcr_abort "不支持的处理器架构：$zcr_arch_source"
    ;;
esac

if [ "$zcr_framework" = "APatch" ] && [ "$zcr_abi" != "arm64-v8a" ]; then
  zcr_abort "APatch 版本仅声明支持 arm64-v8a；当前架构为 $zcr_abi"
fi

zcr_source_dir="$MODPATH/bin/$zcr_abi"
for zcr_name in zcr521d 7zz; do
  [ -s "$zcr_source_dir/$zcr_name" ] ||
    zcr_abort "安装包缺少 $zcr_abi/$zcr_name 的真实可执行文件"
done

zcr_ui "***************************************"
zcr_ui "  ZCR521 AI MCP"
zcr_ui "***************************************"
zcr_ui "- 作者：小骨@Xiaogu_zcr521"
zcr_ui "- 当前版本：0.01"
zcr_ui "- Root 框架：$zcr_framework"
zcr_ui "- Android API：$zcr_api"
zcr_ui "- 处理器架构：$zcr_abi"

zcr_prepare_internal || zcr_abort "无法创建内部状态目录"
zcr_versions="$ZCR_INTERNAL_DIR/versions"
zcr_previous="$zcr_versions/previous/zcr521d"
zcr_previous_version=""
mkdir -p "$zcr_versions/previous" || zcr_abort "无法创建版本回滚目录"
chmod 0700 "$zcr_versions" "$zcr_versions/previous" 2>/dev/null || true

if [ -s "$MODPATH/bin/zcr521d" ]; then
  if ! command -v cmp >/dev/null 2>&1 || ! cmp -s "$MODPATH/bin/zcr521d" "$zcr_source_dir/zcr521d"; then
    zcr_previous_tmp="$zcr_previous.new"
    cp -f "$MODPATH/bin/zcr521d" "$zcr_previous_tmp" ||
      zcr_abort "保存升级前 zcr521d 失败"
    chmod 0755 "$zcr_previous_tmp" || zcr_abort "设置回滚二进制权限失败"
    mv -f "$zcr_previous_tmp" "$zcr_previous" ||
      zcr_abort "提交回滚二进制失败"
    zcr_previous_version="$("$zcr_previous" version 2>/dev/null |
      sed -n 's/.*"version":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
    zcr_ui "- 已保存升级前版本：${zcr_previous_version:-unknown}"
  fi
fi

mkdir -p "$MODPATH/bin" || zcr_abort "无法创建程序目录"
for zcr_name in zcr521d 7zz; do
  zcr_tmp="$MODPATH/bin/$zcr_name.new"
  cp -f "$zcr_source_dir/$zcr_name" "$zcr_tmp" ||
    zcr_abort "复制 $zcr_name 失败"
  chmod 0755 "$zcr_tmp" || zcr_abort "设置 $zcr_name 执行权限失败"
  chown 0:0 "$zcr_tmp" 2>/dev/null || true
  mv -f "$zcr_tmp" "$MODPATH/bin/$zcr_name" ||
    zcr_abort "部署 $zcr_name 失败"
done

for zcr_dir in arm64-v8a armeabi-v7a x86_64; do
  rm -rf "$MODPATH/bin/$zcr_dir"
done

if command -v set_perm_recursive >/dev/null 2>&1; then
  set_perm_recursive "$MODPATH" 0 0 0755 0644
fi
for zcr_script in \
  common.sh customize.sh post-fs-data.sh service.sh action.sh uninstall.sh systemless.sh; do
  [ -f "$MODPATH/$zcr_script" ] || continue
  if command -v set_perm >/dev/null 2>&1; then
    set_perm "$MODPATH/$zcr_script" 0 0 0755
  else
    chown 0:0 "$MODPATH/$zcr_script" 2>/dev/null || true
    chmod 0755 "$MODPATH/$zcr_script" 2>/dev/null || true
  fi
done
chmod 0755 "$MODPATH/bin/zcr521d" "$MODPATH/bin/7zz" 2>/dev/null || true

zcr_candidate_version="$(sed -n 's/^version=//p' "$MODPATH/module.prop" | head -n 1)"
zcr_candidate_started="$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || printf '1970-01-01T00:00:00Z')"
zcr_state_tmp="$zcr_versions/state.json.new"
cat > "$zcr_state_tmp" <<EOF
{
  "activeVersion": "",
  "activeBinary": "$MODPATH/bin/zcr521d",
  "candidateVersion": "$zcr_candidate_version",
  "candidateBinary": "$MODPATH/bin/zcr521d",
  "previousVersion": "$zcr_previous_version",
  "previousBinary": "$([ -s "$zcr_previous" ] && printf '%s' "$zcr_previous")",
  "candidateStarted": "$zcr_candidate_started"
}
EOF
chmod 0600 "$zcr_state_tmp" || zcr_abort "设置版本状态权限失败"
mv -f "$zcr_state_tmp" "$zcr_versions/state.json" ||
  zcr_abort "提交版本状态失败"

zcr_prepare_work_dirs || zcr_ui "- 共享存储尚未就绪，将在开机后自动创建工作目录"
zcr_write_mcp_address || zcr_ui "- MCP 地址文件暂未生成，将在服务启动时重试"
printf '%s\n' "$zcr_framework" > "$ZCR_INTERNAL_DIR/installed-framework"
printf '%s\n' "$zcr_abi" > "$ZCR_INTERNAL_DIR/installed-abi"
chmod 0600 "$ZCR_INTERNAL_DIR/installed-framework" "$ZCR_INTERNAL_DIR/installed-abi" 2>/dev/null || true

zcr_ui "- 默认端口：5322"
zcr_ui "- 工作目录：/storage/emulated/0/zcr521AI"
zcr_ui "- MCP 地址文件：/data/adb/zcr521-mcp/MCP地址.txt"
zcr_ui "- 安装完成，请重启设备"

am start --user 0 -a android.intent.action.VIEW \
  -d "tg://resolve?domain=Xiaogu_zcr521" >/dev/null 2>&1 || true
