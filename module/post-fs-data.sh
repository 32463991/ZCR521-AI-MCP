#!/system/bin/sh

MODDIR=${0%/*}
ZCR_MODDIR="$MODDIR"
. "$MODDIR/common.sh"

umask 077
zcr_prepare_internal || exit 0

zcr_existing_pid="$(zcr_read_pid 2>/dev/null)"
if [ -z "$zcr_existing_pid" ] || ! zcr_pid_is_supervisor "$zcr_existing_pid"; then
  rm -f "$ZCR_PID_FILE" "$ZCR_RUN_DIR/control.sock" "$ZCR_RUN_DIR/http.sock"
  rm -f "$ZCR_START_LOCK/owner" 2>/dev/null || true
  rmdir "$ZCR_START_LOCK" 2>/dev/null || true
fi

zcr_framework="$(zcr_detect_framework)"
zcr_tmp="$ZCR_INTERNAL_DIR/framework.tmp.$$"
{
  printf 'framework=%s\n' "$zcr_framework"
  printf 'version=%s\n' "$(zcr_framework_version)"
  printf 'android_api=%s\n' "$(getprop ro.build.version.sdk 2>/dev/null)"
  printf 'boot_id=%s\n' "$(cat /proc/sys/kernel/random/boot_id 2>/dev/null)"
} > "$zcr_tmp"
chmod 0600 "$zcr_tmp" 2>/dev/null || true
mv -f "$zcr_tmp" "$ZCR_INTERNAL_DIR/framework.info"

exit 0
