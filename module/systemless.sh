#!/system/bin/sh

MODDIR=${0%/*}
ZCR_MODDIR="$MODDIR"
. "$MODDIR/common.sh"

zcr_systemless_usage() {
  zcr_print "用法："
  zcr_print "  systemless.sh probe"
  zcr_print "  systemless.sh stage-file <源文件> <系统绝对路径>"
  zcr_print "  systemless.sh remove-file <系统绝对路径>"
  zcr_print "  systemless.sh bind <源路径> <目标路径>"
  zcr_print "  systemless.sh unbind <目标路径>"
}

zcr_systemless_target() {
  zcr_target="$1"
  case "$zcr_target" in
    *"/../"*|*/..|../*|*//*)
      return 1
      ;;
    /system/*)
      zcr_print "$MODDIR/system/${zcr_target#/system/}"
      ;;
    /system_ext/*)
      zcr_print "$MODDIR/system/system_ext/${zcr_target#/system_ext/}"
      ;;
    /product/*)
      zcr_print "$MODDIR/system/product/${zcr_target#/product/}"
      ;;
    /vendor/*)
      zcr_print "$MODDIR/system/vendor/${zcr_target#/vendor/}"
      ;;
    /odm/*)
      zcr_print "$MODDIR/system/odm/${zcr_target#/odm/}"
      ;;
    *)
      return 1
      ;;
  esac
}

zcr_systemless_probe() {
  zcr_framework="$(zcr_detect_framework)"
  zcr_print "framework=$zcr_framework"
  zcr_print "module_system=$MODDIR/system"
  case "$zcr_framework" in
    Magisk)
      zcr_print "persistent_mount_strategy=magisk-magic-mount"
      ;;
    "KernelSU"|"KernelSU Next")
      zcr_print "persistent_mount_strategy=kernelsu-metamodule-dependent"
      ;;
    APatch)
      zcr_print "persistent_mount_strategy=apatch-overlayfs"
      ;;
    *)
      zcr_print "persistent_mount_strategy=manager-capability-dependent"
      ;;
  esac
  if grep -qw overlay /proc/filesystems 2>/dev/null; then
    zcr_print "overlayfs=yes"
  else
    zcr_print "overlayfs=no"
  fi
  if zcr_command_exists mount; then
    zcr_print "bind_mount=yes"
  else
    zcr_print "bind_mount=no"
  fi
  zcr_print "selinux=$(getenforce 2>/dev/null)"
  zcr_print "real_partition_remount=never-by-default"
}

zcr_systemless_stage_file() {
  zcr_source="$1"
  zcr_target="$2"
  [ -f "$zcr_source" ] || {
    zcr_print "源文件不存在或不是普通文件：$zcr_source"
    return 1
  }
  zcr_module_target="$(zcr_systemless_target "$zcr_target")" || {
    zcr_print "仅允许 /system、/system_ext、/product、/vendor、/odm 下的绝对路径"
    return 1
  }
  zcr_parent=${zcr_module_target%/*}
  mkdir -p "$zcr_parent" || return 1
  zcr_tmp="$zcr_module_target.tmp.$$"
  cp -p "$zcr_source" "$zcr_tmp" || return 1
  chown 0:0 "$zcr_tmp" 2>/dev/null || true
  mv -f "$zcr_tmp" "$zcr_module_target" || return 1
  zcr_print "已写入 Systemless 覆盖：$zcr_target"
  case "$(zcr_detect_framework)" in
    "KernelSU"|"KernelSU Next")
      zcr_print "KernelSU 仅在已配置可用挂载 metamodule 时生效；核心 MCP 服务不依赖该覆盖"
      ;;
    *)
      zcr_print "重启后由 Root 框架挂载生效"
      ;;
  esac
}

zcr_systemless_remove_file() {
  zcr_target="$1"
  zcr_module_target="$(zcr_systemless_target "$zcr_target")" || {
    zcr_print "目标路径不在允许的 Systemless 范围"
    return 1
  }
  if [ -d "$zcr_module_target" ]; then
    zcr_print "拒绝递归删除目录；请逐个删除文件"
    return 1
  fi
  rm -f "$zcr_module_target" || return 1
  zcr_print "已移除 Systemless 文件：$zcr_target"
  zcr_print "重启后生效"
}

zcr_systemless_bind() {
  zcr_source="$1"
  zcr_target="$2"
  [ -e "$zcr_source" ] || {
    zcr_print "源路径不存在：$zcr_source"
    return 1
  }
  [ -e "$zcr_target" ] || {
    zcr_print "目标路径不存在：$zcr_target"
    return 1
  }
  mount -o bind "$zcr_source" "$zcr_target" || return 1
  zcr_print "临时 Bind Mount 已生效；重启后自动失效"
}

zcr_systemless_unbind() {
  zcr_target="$1"
  awk -v target="$zcr_target" '$2 == target {found=1} END {exit found ? 0 : 1}' \
    /proc/mounts 2>/dev/null || {
      zcr_print "目标不是当前挂载点：$zcr_target"
      return 1
    }
  umount "$zcr_target" || return 1
  zcr_print "已卸载临时挂载：$zcr_target"
}

case "${1:-probe}" in
  probe)
    zcr_systemless_probe
    ;;
  stage-file)
    [ "$#" -eq 3 ] || {
      zcr_systemless_usage
      exit 2
    }
    zcr_systemless_stage_file "$2" "$3"
    ;;
  remove-file)
    [ "$#" -eq 2 ] || {
      zcr_systemless_usage
      exit 2
    }
    zcr_systemless_remove_file "$2"
    ;;
  bind)
    [ "$#" -eq 3 ] || {
      zcr_systemless_usage
      exit 2
    }
    zcr_systemless_bind "$2" "$3"
    ;;
  unbind)
    [ "$#" -eq 2 ] || {
      zcr_systemless_usage
      exit 2
    }
    zcr_systemless_unbind "$2"
    ;;
  *)
    zcr_systemless_usage
    exit 2
    ;;
esac
