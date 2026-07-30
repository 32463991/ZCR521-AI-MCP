#!/system/bin/sh

MODDIR=${0%/*}
ZCR_MODDIR="$MODDIR"
. "$MODDIR/common.sh"

zcr_prepare_internal >/dev/null 2>&1 || true
zcr_write_mcp_address >/dev/null 2>&1 || true
zcr_print '作者:小骨@Xiaogu_zcr521'
zcr_print '当前版本: 0.01'
zcr_print ''
zcr_print_summary
exit 0
