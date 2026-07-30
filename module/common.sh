#!/system/bin/sh

ZCR_MODULE_ID="zcr521.android.mcp"
ZCR_MODDIR="${ZCR_MODDIR:-${0%/*}}"
ZCR_INTERNAL_DIR="/data/adb/zcr521-mcp"
ZCR_RUN_DIR="$ZCR_INTERNAL_DIR/run"
ZCR_LOG_DIR="$ZCR_INTERNAL_DIR/logs"
ZCR_PID_FILE="$ZCR_RUN_DIR/supervisor.pid"
ZCR_START_LOCK="$ZCR_RUN_DIR/start.lock"
ZCR_MANUAL_STOP="$ZCR_INTERNAL_DIR/manual-stop"
ZCR_CRASH_BLOCK="$ZCR_INTERNAL_DIR/crash-loop.blocked"
ZCR_EFFECTIVE_ENV="$ZCR_RUN_DIR/effective.env"
ZCR_WORK_DIR="/storage/emulated/0/zcr521AI"
ZCR_DAEMON="$ZCR_MODDIR/bin/zcr521d"
ZCR_7ZZ="$ZCR_MODDIR/bin/7zz"
ZCR_DEFAULT_PORT="5322"
ZCR_ADDRESS_FILE="/data/adb/zcr521-mcp/MCP地址.txt"

PATH="/data/adb/magisk:/data/adb/ksu/bin:/data/adb/ap/bin:/system/bin:/system/xbin:/vendor/bin:$PATH"
export PATH

zcr_print() {
  printf '%s\n' "$*"
}

zcr_command_exists() {
  command -v "$1" >/dev/null 2>&1
}

zcr_prepare_internal() {
  umask 077
  mkdir -p "$ZCR_INTERNAL_DIR" "$ZCR_RUN_DIR" "$ZCR_LOG_DIR" 2>/dev/null || return 1
  chown 0:2000 "$ZCR_INTERNAL_DIR" "$ZCR_RUN_DIR" 2>/dev/null || true
  chown 0:0 "$ZCR_LOG_DIR" 2>/dev/null || true
  chmod 0710 "$ZCR_INTERNAL_DIR" 2>/dev/null || true
  chmod 0710 "$ZCR_RUN_DIR" 2>/dev/null || true
  chmod 0700 "$ZCR_LOG_DIR" 2>/dev/null || true
}

zcr_prepare_work_dirs() {
  mkdir -p "$ZCR_WORK_DIR" 2>/dev/null
}

zcr_write_mcp_address() {
  zcr_prepare_internal || return 1
  zcr_address_ip="$(zcr_lan_ip 2>/dev/null)"
  zcr_address_port="$(zcr_port)"
  zcr_address_tmp="$ZCR_ADDRESS_FILE.tmp.$$"
  umask 022
  {
    printf '本机地址：http://127.0.0.1:%s/mcp\n' "$zcr_address_port"
    if [ -n "$zcr_address_ip" ]; then
      printf '局域网地址：http://%s:%s/mcp\n' "$zcr_address_ip" "$zcr_address_port"
    fi
  } > "$zcr_address_tmp" || return 1
  chown 0:0 "$zcr_address_tmp" 2>/dev/null || true
  chmod 0644 "$zcr_address_tmp" 2>/dev/null || true
  mv -f "$zcr_address_tmp" "$ZCR_ADDRESS_FILE"
}

zcr_detect_framework() {
  if [ "${APATCH:-}" = "true" ] ||
     [ -n "${APATCH_VER_CODE:-}" ] ||
     [ -x /data/adb/ap/bin/apd ] ||
     zcr_command_exists apd; then
    zcr_print "APatch"
    return 0
  fi

  if [ "${KSU:-}" = "true" ] ||
     [ -n "${KSU_VER_CODE:-}" ] ||
     [ -x /data/adb/ksu/bin/ksud ] ||
     zcr_command_exists ksud; then
    zcr_ksu_probe=""
    if [ -x /data/adb/ksu/bin/ksud ]; then
      zcr_ksu_probe="$(/data/adb/ksu/bin/ksud -V 2>/dev/null | head -n 1)"
    elif zcr_command_exists ksud; then
      zcr_ksu_probe="$(ksud -V 2>/dev/null | head -n 1)"
    fi
    case "${KSU_NEXT:-} ${KSU_VARIANT:-} ${KSU_VER:-} ${KSU_MANAGER_PACKAGE:-} $zcr_ksu_probe" in
      *[Nn]ext*|*ksunext*|*rifsxd*)
        zcr_print "KernelSU Next"
        ;;
      *)
        zcr_print "KernelSU"
        ;;
    esac
    return 0
  fi

  if [ -n "${MAGISK_VER_CODE:-}" ] ||
     [ -x /data/adb/magisk/magisk ] ||
     zcr_command_exists magisk; then
    zcr_print "Magisk"
    return 0
  fi

  zcr_print "Magisk-compatible/unknown"
}

zcr_framework_version() {
  case "$(zcr_detect_framework)" in
    APatch)
      if [ -n "${APATCH_VER:-}" ]; then
        zcr_print "$APATCH_VER"
      elif [ -x /data/adb/ap/bin/apd ]; then
        /data/adb/ap/bin/apd --version 2>/dev/null | head -n 1
      else
        zcr_print "unknown"
      fi
      ;;
    KernelSU*)
      if [ -n "${KSU_VER:-}" ]; then
        zcr_print "$KSU_VER"
      elif [ -x /data/adb/ksu/bin/ksud ]; then
        /data/adb/ksu/bin/ksud -V 2>/dev/null | head -n 1
      else
        zcr_print "unknown"
      fi
      ;;
    Magisk)
      if [ -n "${MAGISK_VER:-}" ]; then
        zcr_print "$MAGISK_VER"
      elif zcr_command_exists magisk; then
        magisk -v 2>/dev/null | head -n 1
      else
        zcr_print "unknown"
      fi
      ;;
    *)
      zcr_print "unknown"
      ;;
  esac
}

zcr_module_version() {
  if [ -r "$ZCR_MODDIR/module.prop" ]; then
    sed -n 's/^version=//p' "$ZCR_MODDIR/module.prop" 2>/dev/null | head -n 1
  else
    zcr_print "unknown"
  fi
}

zcr_read_pid() {
  [ -r "$ZCR_PID_FILE" ] || return 1
  IFS= read -r zcr_pid < "$ZCR_PID_FILE" || return 1
  case "$zcr_pid" in
    ''|*[!0-9]*|0)
      return 1
      ;;
  esac
  zcr_print "$zcr_pid"
}

zcr_pid_is_supervisor() {
  zcr_pid="$1"
  case "$zcr_pid" in
    ''|*[!0-9]*|0)
      return 1
      ;;
  esac

  [ -d "/proc/$zcr_pid" ] || return 1
  kill -0 "$zcr_pid" 2>/dev/null || return 1

  zcr_uid="$(awk '/^Uid:/{print $2; exit}' "/proc/$zcr_pid/status" 2>/dev/null)"
  [ "$zcr_uid" = "0" ] || return 1

  zcr_exe="$(readlink "/proc/$zcr_pid/exe" 2>/dev/null)"
  case "$zcr_exe" in
    "$ZCR_DAEMON"|"$ZCR_DAEMON (deleted)")
      ;;
    *)
      return 1
      ;;
  esac

  zcr_cmdline="$(tr '\000' ' ' < "/proc/$zcr_pid/cmdline" 2>/dev/null)"
  case "$zcr_cmdline" in
    *zcr521d*supervisor*)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

zcr_write_pid() {
  zcr_pid="$1"
  zcr_tmp="$ZCR_PID_FILE.$$"
  umask 077
  printf '%s\n' "$zcr_pid" > "$zcr_tmp" || return 1
  chmod 0600 "$zcr_tmp" 2>/dev/null || true
  mv -f "$zcr_tmp" "$ZCR_PID_FILE"
}

zcr_rotate_start_log() {
  zcr_start_log="$ZCR_LOG_DIR/supervisor.log"
  [ -f "$zcr_start_log" ] || return 0
  zcr_size="$(wc -c < "$zcr_start_log" 2>/dev/null)"
  case "$zcr_size" in
    ''|*[!0-9]*)
      return 0
      ;;
  esac
  if [ "$zcr_size" -gt 4194304 ]; then
    mv -f "$zcr_start_log" "$zcr_start_log.1" 2>/dev/null || true
  fi
}

zcr_start_supervisor() {
  zcr_mode="${1:-auto}"
  zcr_prepare_internal || {
    zcr_print "无法创建内部运行目录：$ZCR_INTERNAL_DIR"
    return 1
  }
  zcr_write_mcp_address >/dev/null 2>&1 || true

  if [ "$zcr_mode" = "force" ]; then
    rm -f "$ZCR_MANUAL_STOP" "$ZCR_CRASH_BLOCK"
  elif [ -e "$ZCR_MANUAL_STOP" ]; then
    zcr_print "服务已被用户手动停止，本次不开机启动"
    return 0
  elif [ -e "$ZCR_CRASH_BLOCK" ]; then
    zcr_print "服务因连续崩溃被安全暂停，请从模块操作菜单手动启动"
    return 0
  fi

  [ -x "$ZCR_DAEMON" ] || {
    zcr_print "主程序不存在或不可执行：$ZCR_DAEMON"
    return 1
  }

  zcr_old_pid="$(zcr_read_pid 2>/dev/null)"
  if [ -n "$zcr_old_pid" ] && zcr_pid_is_supervisor "$zcr_old_pid"; then
    zcr_print "服务已运行，PID=$zcr_old_pid"
    return 0
  fi
  rm -f "$ZCR_PID_FILE"

  if ! mkdir "$ZCR_START_LOCK" 2>/dev/null; then
    zcr_lock_owner=""
    if [ -r "$ZCR_START_LOCK/owner" ]; then
      IFS= read -r zcr_lock_owner < "$ZCR_START_LOCK/owner" || true
    fi
    case "$zcr_lock_owner" in
      ''|*[!0-9]*|0)
        ;;
      *)
        if kill -0 "$zcr_lock_owner" 2>/dev/null; then
          zcr_print "另一启动流程正在执行"
          return 0
        fi
        ;;
    esac
    rm -f "$ZCR_START_LOCK/owner" 2>/dev/null || true
    rmdir "$ZCR_START_LOCK" 2>/dev/null || {
      zcr_print "无法取得启动锁"
      return 1
    }
    mkdir "$ZCR_START_LOCK" 2>/dev/null || return 1
  fi

  printf '%s\n' "$$" > "$ZCR_START_LOCK/owner"
  trap 'rm -f "$ZCR_START_LOCK/owner" 2>/dev/null; rmdir "$ZCR_START_LOCK" 2>/dev/null' EXIT HUP INT TERM

  zcr_rotate_start_log
  zcr_framework="$(zcr_detect_framework)"
  export ZCR521_MODULE_DIR="$ZCR_MODDIR"
  export ZCR521_STATE_DIR="$ZCR_INTERNAL_DIR"
  export ZCR521_WORK_DIR="$ZCR_WORK_DIR"
  export ZCR521_ROOT_FRAMEWORK="$zcr_framework"
  export ZCR521_7ZZ="$ZCR_7ZZ"

  if zcr_command_exists nohup; then
    nohup "$ZCR_DAEMON" supervisor \
      >> "$ZCR_LOG_DIR/supervisor.log" 2>&1 < /dev/null &
  else
    "$ZCR_DAEMON" supervisor \
      >> "$ZCR_LOG_DIR/supervisor.log" 2>&1 < /dev/null &
  fi
  zcr_new_pid=$!
  zcr_write_pid "$zcr_new_pid" || {
    if zcr_pid_is_supervisor "$zcr_new_pid"; then
      kill -TERM "$zcr_new_pid" 2>/dev/null || true
    fi
    return 1
  }

  zcr_print "服务启动请求已提交，PID=$zcr_new_pid"
  return 0
}

zcr_stop_supervisor() {
  zcr_pid="$(zcr_read_pid 2>/dev/null)"
  if [ -z "$zcr_pid" ]; then
    rm -f "$ZCR_PID_FILE"
    zcr_print "服务未运行"
    return 0
  fi

  if ! zcr_pid_is_supervisor "$zcr_pid"; then
    rm -f "$ZCR_PID_FILE"
    zcr_print "PID 文件已失效；为避免误杀，未向任何进程发送信号"
    return 0
  fi

  kill -TERM "$zcr_pid" 2>/dev/null || true
  zcr_wait=0
  while [ "$zcr_wait" -lt 10 ]; do
    if ! zcr_pid_is_supervisor "$zcr_pid"; then
      rm -f "$ZCR_PID_FILE"
      zcr_print "服务已停止"
      return 0
    fi
    sleep 1
    zcr_wait=$((zcr_wait + 1))
  done

  if zcr_pid_is_supervisor "$zcr_pid"; then
    kill -KILL "$zcr_pid" 2>/dev/null || true
    sleep 1
  fi

  if zcr_pid_is_supervisor "$zcr_pid"; then
    zcr_print "服务仍未停止，保留 PID 文件供诊断"
    return 1
  fi

  rm -f "$ZCR_PID_FILE"
  zcr_print "服务已强制停止"
}

zcr_port() {
  zcr_port_value=""
  if [ -r "$ZCR_EFFECTIVE_ENV" ]; then
    zcr_port_value="$(sed -n 's/^PORT=\([0-9][0-9]*\)$/\1/p' "$ZCR_EFFECTIVE_ENV" 2>/dev/null | head -n 1)"
  fi
  case "$zcr_port_value" in
    ''|*[!0-9]*)
      zcr_port_value="$ZCR_DEFAULT_PORT"
      ;;
  esac
  if [ "$zcr_port_value" -lt 1 ] || [ "$zcr_port_value" -gt 65535 ]; then
    zcr_port_value="$ZCR_DEFAULT_PORT"
  fi
  zcr_print "$zcr_port_value"
}

zcr_ip_for_interface() {
  zcr_iface="$1"
  zcr_ip=""
  if zcr_command_exists ip; then
    zcr_ip="$(ip -o -4 addr show dev "$zcr_iface" 2>/dev/null |
      awk '$4 !~ /^127[.]/ {split($4,a,"/"); print a[1]; exit}')"
    if [ -z "$zcr_ip" ]; then
      zcr_ip="$(ip -4 addr show dev "$zcr_iface" 2>/dev/null |
        awk '/inet / && $2 !~ /^127[.]/ {split($2,a,"/"); print a[1]; exit}')"
    fi
  fi
  [ -n "$zcr_ip" ] || return 1
  zcr_print "$zcr_ip"
}

zcr_lan_ip() {
  for zcr_iface in wlan0 wlan1 ap0 swlan0 eth0 eth1 rndis0 usb0 bt-pan; do
    zcr_ip="$(zcr_ip_for_interface "$zcr_iface" 2>/dev/null)"
    if [ -n "$zcr_ip" ]; then
      zcr_print "$zcr_ip"
      return 0
    fi
  done

  if zcr_command_exists ip; then
    zcr_ip="$(ip -o -4 addr show 2>/dev/null |
      awk '$2 !~ /^(lo|rmnet|ccmni|pdp|wwan|cellular|v4-rmnet|r_rmnet)/ &&
           $4 !~ /^127[.]/ {split($4,a,"/"); print a[1]; exit}')"
    if [ -z "$zcr_ip" ]; then
      zcr_ip="$(ip -4 addr show 2>/dev/null |
        awk '/^[0-9]+: / {
               iface=$2
               sub(/:$/,"",iface)
             }
             /inet / && iface !~ /^(lo|rmnet|ccmni|pdp|wwan|cellular|v4-rmnet|r_rmnet)/ &&
             $2 !~ /^127[.]/ {split($2,a,"/"); print a[1]; exit}')"
    fi
    if [ -n "$zcr_ip" ]; then
      zcr_print "$zcr_ip"
      return 0
    fi
  fi

  if zcr_command_exists ifconfig; then
    zcr_ip="$(ifconfig 2>/dev/null |
      awk '$0 !~ /^[ \t]/ {
        iface=$1
        sub(/:$/,"",iface)
      }
      /inet (addr:)?/ && iface !~ /^(lo|rmnet|ccmni|pdp|wwan|cellular|v4-rmnet|r_rmnet)/ {
        for (i=1;i<=NF;i++) {
          if ($i ~ /^addr:/) {sub(/^addr:/,"",$i); ip=$i}
          else if ($i ~ /^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/) {ip=$i}
          if (ip != "" && ip !~ /^127[.]/) {print ip; exit}
        }
      }')"
    if [ -n "$zcr_ip" ]; then
      zcr_print "$zcr_ip"
      return 0
    fi
  fi

  if [ -r "$ZCR_ADDRESS_FILE" ]; then
    zcr_ip="$(sed -n 's#^局域网地址：http://\([0-9][0-9.]*\):[0-9][0-9]*/mcp$#\1#p' \
      "$ZCR_ADDRESS_FILE" 2>/dev/null | head -n 1)"
    if [ -n "$zcr_ip" ]; then
      zcr_print "$zcr_ip"
      return 0
    fi
  fi
  return 1
}

zcr_port_is_listening() {
  zcr_listen_port="$(zcr_port)"
  zcr_hex_port="$(printf '%04X' "$zcr_listen_port" 2>/dev/null)"
  [ -n "$zcr_hex_port" ] || return 1
  awk -v suffix=":$zcr_hex_port" '
    $2 ~ suffix "$" && $4 == "0A" {found=1}
    END {exit found ? 0 : 1}
  ' /proc/net/tcp /proc/net/tcp6 2>/dev/null
}

zcr_print_summary() {
  zcr_pid="$(zcr_read_pid 2>/dev/null)"
  if [ -n "$zcr_pid" ] && zcr_pid_is_supervisor "$zcr_pid"; then
    zcr_service="运行中（PID=$zcr_pid）"
  else
    zcr_service="未运行"
  fi

  if [ "$(id -u 2>/dev/null)" = "0" ]; then
    zcr_root="可用（uid=0）"
  else
    zcr_root="不可用"
  fi

  zcr_ip="$(zcr_lan_ip 2>/dev/null)"
  if [ -n "$zcr_ip" ]; then
    zcr_mcp_host="$zcr_ip"
  else
    zcr_ip="未连接局域网"
    zcr_mcp_host="127.0.0.1"
  fi
  zcr_port_value="$(zcr_port)"

  zcr_print "服务状态：$zcr_service"
  zcr_print "Root 状态：$zcr_root"
  zcr_print "Android 版本：$(getprop ro.build.version.release 2>/dev/null)（API $(getprop ro.build.version.sdk 2>/dev/null)）"
  zcr_print "当前 Root 框架：$(zcr_detect_framework)"
  zcr_print "当前 IP：$zcr_ip"
  zcr_print "MCP 地址：http://$zcr_mcp_host:$zcr_port_value/mcp"
  zcr_print "默认端口：$ZCR_DEFAULT_PORT"
  zcr_print "默认工作目录：$ZCR_WORK_DIR"
  zcr_print "模块版本：$(zcr_module_version)"
}
