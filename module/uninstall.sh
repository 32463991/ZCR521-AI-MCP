#!/system/bin/sh

MODDIR=${0%/*}
ZCR_MODDIR="$MODDIR"
. "$MODDIR/common.sh"

zcr_prepare_internal >/dev/null 2>&1 || true
touch "$ZCR_MANUAL_STOP" 2>/dev/null || true
if ! zcr_stop_supervisor; then
  zcr_print "无法确认 supervisor 已退出；为保留诊断证据，未删除内部状态"
  exit 1
fi

case "$ZCR_INTERNAL_DIR" in
  /data/adb/zcr521-mcp)
    rm -rf /data/adb/zcr521-mcp
    ;;
  *)
    zcr_print "内部目录校验失败，跳过清理"
    ;;
esac

zcr_print "已停止 ZCR521 服务并清理内部状态"
zcr_print "已保留用户目录：$ZCR_WORK_DIR"
exit 0
