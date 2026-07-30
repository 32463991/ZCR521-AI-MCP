#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$SCRIPT_DIR/SOURCE.lock"

API_LEVEL=${ANDROID_API_LEVEL:-26}
SOURCE_DIR=${SEVENZIP_SOURCE_DIR:-"$SCRIPT_DIR/.cache/7zip-$SEVENZIP_VERSION"}
BUILD_DIR=${SEVENZIP_BUILD_DIR:-"$SCRIPT_DIR/build"}
OUT_DIR=${SEVENZIP_OUT_DIR:-"$SCRIPT_DIR/out"}
JOBS=${JOBS:-}
REQUESTED_ABIS=${SEVENZIP_ABIS:-"arm64-v8a armeabi-v7a x86_64"}

fail() {
  printf '错误：%s\n' "$*" >&2
  exit 1
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$1" | sed 's/^.*= //'
  else
    fail "缺少 SHA-256 工具"
  fi
}

find_tool() {
  tool_base=$1
  for tool_candidate in "$tool_base" "$tool_base.exe" "$tool_base.cmd"; do
    if [ -x "$tool_candidate" ] || [ -f "$tool_candidate" ]; then
      printf '%s\n' "$tool_candidate"
      return 0
    fi
  done
  return 1
}

NDK=${ANDROID_NDK_HOME:-${ANDROID_NDK_ROOT:-}}
[ -n "$NDK" ] || fail "请设置 ANDROID_NDK_HOME，且必须指向 Android NDK r29"
[ -r "$NDK/source.properties" ] || fail "无效的 NDK 目录：$NDK"

NDK_REVISION=$(sed -n 's/^Pkg[.]Revision[[:space:]]*=[[:space:]]*//p' "$NDK/source.properties" | head -n 1)
case "$NDK_REVISION" in
  29.*)
    ;;
  *)
    fail "要求 Android NDK r29，当前版本为 ${NDK_REVISION:-unknown}"
    ;;
esac

case "$API_LEVEL" in
  ''|*[!0-9]*)
    fail "ANDROID_API_LEVEL 必须是整数"
    ;;
esac
[ "$API_LEVEL" -ge 26 ] || fail "最低 Android API Level 为 26"

PREBUILT_ROOT="$NDK/toolchains/llvm/prebuilt"
[ -d "$PREBUILT_ROOT" ] || fail "NDK 缺少 LLVM 预构建工具链"

if [ -n "${NDK_HOST_TAG:-}" ]; then
  TOOLCHAIN="$PREBUILT_ROOT/$NDK_HOST_TAG"
else
  TOOLCHAIN=""
  for toolchain_candidate in "$PREBUILT_ROOT"/*; do
    [ -d "$toolchain_candidate" ] || continue
    if find_tool "$toolchain_candidate/bin/clang" >/dev/null 2>&1; then
      TOOLCHAIN="$toolchain_candidate"
      break
    fi
  done
fi
[ -n "$TOOLCHAIN" ] && [ -d "$TOOLCHAIN" ] ||
  fail "无法为当前主机找到 NDK LLVM 工具链；可设置 NDK_HOST_TAG"

LLVM_AR=$(find_tool "$TOOLCHAIN/bin/llvm-ar") || fail "NDK 缺少 llvm-ar"
LLVM_RANLIB=$(find_tool "$TOOLCHAIN/bin/llvm-ranlib") || fail "NDK 缺少 llvm-ranlib"
LLVM_STRIP=$(find_tool "$TOOLCHAIN/bin/llvm-strip") || fail "NDK 缺少 llvm-strip"
LLVM_READELF=$(find_tool "$TOOLCHAIN/bin/llvm-readelf") || fail "NDK 缺少 llvm-readelf"

command -v make >/dev/null 2>&1 || fail "主机缺少 GNU make"

if [ -z "$JOBS" ]; then
  if command -v getconf >/dev/null 2>&1; then
    JOBS=$(getconf _NPROCESSORS_ONLN 2>/dev/null || printf '1')
  else
    JOBS=1
  fi
fi
case "$JOBS" in
  ''|*[!0-9]*|0)
    JOBS=1
    ;;
esac

for requested_abi in $REQUESTED_ABIS; do
  case "$requested_abi" in
    arm64-v8a|armeabi-v7a|x86_64)
      ;;
    *)
      fail "SEVENZIP_ABIS 含未知 ABI：$requested_abi"
      ;;
  esac
done

"$SCRIPT_DIR/fetch-source.sh" "$SOURCE_DIR" >/dev/null

[ -n "$BUILD_DIR" ] && [ "$BUILD_DIR" != "/" ] ||
  fail "拒绝使用不安全的构建目录"
[ -n "$OUT_DIR" ] && [ "$OUT_DIR" != "/" ] ||
  fail "拒绝使用不安全的输出目录"
case "$NDK:$SOURCE_DIR:$BUILD_DIR:$OUT_DIR" in
  *" "*)
    fail "7-Zip 上游 makefile 不支持含空格路径；请使用无空格的 NDK、源码和输出目录"
    ;;
esac
mkdir -p "$BUILD_DIR/obj" "$OUT_DIR"

build_abi() {
  abi=$1
  target_prefix=$2
  expected_machine=$3
  expected_linker=$4

  cc=$(find_tool "$TOOLCHAIN/bin/${target_prefix}${API_LEVEL}-clang") ||
    fail "缺少编译器：${target_prefix}${API_LEVEL}-clang"
  cxx=$(find_tool "$TOOLCHAIN/bin/${target_prefix}${API_LEVEL}-clang++") ||
    fail "缺少编译器：${target_prefix}${API_LEVEL}-clang++"

  object_dir="$BUILD_DIR/obj/$abi"
  output_abi_dir="$OUT_DIR/$abi"
  case "$object_dir:$output_abi_dir" in
    "$BUILD_DIR"/*:"$OUT_DIR"/*)
      rm -rf "$object_dir" "$output_abi_dir"
      ;;
    *)
      fail "ABI 构建目录超出受控范围"
      ;;
  esac
  mkdir -p "$object_dir" "$output_abi_dir"

  printf '正在构建 7-Zip %s：%s（API %s）\n' "$SEVENZIP_VERSION" "$abi" "$API_LEVEL"
  (
    cd "$SOURCE_DIR/CPP/7zip/Bundles/Alone2"
    make -j"$JOBS" \
      -f ../../cmpl_clang.mak \
      -f "$SCRIPT_DIR/android-link-response.mak" \
      O="$object_dir" \
      SystemDrive= \
      SYSTEMDRIVE= \
      OS= \
      IS_MINGW= \
      CC="$cc" \
      CXX="$cxx" \
      AR="$LLVM_AR" \
      RANLIB="$LLVM_RANLIB" \
      CFLAGS_WARN= \
      CFLAGS_WARN_WALL="-Wall -Wextra" \
      CFLAGS_BASE2="-fPIE -ffunction-sections -fdata-sections" \
      LDFLAGS_STATIC_3="-pie -static-libstdc++ -Wl,--gc-sections -Wl,-z,max-page-size=16384 -Wl,-z,common-page-size=16384" \
      LIB2="-ldl"
  )

  built="$object_dir/7zz"
  [ -s "$built" ] || fail "$abi 未生成真实 7zz"
  cp -f "$built" "$output_abi_dir/7zz.unstripped"
  "$LLVM_STRIP" --strip-unneeded -o "$output_abi_dir/7zz" "$built"
  chmod 0755 "$output_abi_dir/7zz"

  "$LLVM_READELF" -h "$output_abi_dir/7zz" |
    grep -F "Machine:" |
    grep -F "$expected_machine" >/dev/null ||
    fail "$abi 的 ELF Machine 校验失败"

  "$LLVM_READELF" -l "$output_abi_dir/7zz" |
    grep -F "$expected_linker" >/dev/null ||
    fail "$abi 的 Android 动态链接器校验失败"

  load_alignments=$("$LLVM_READELF" -lW "$output_abi_dir/7zz" |
    awk '$1 == "LOAD" {print $NF}')
  load_count=0
  while IFS= read -r load_alignment; do
    [ -n "$load_alignment" ] || continue
    case "$load_alignment" in
      0x[0-9A-Fa-f]*)
        ;;
      *)
        fail "$abi 出现无法识别的 PT_LOAD p_align：$load_alignment"
        ;;
    esac
    load_alignment_value=$((load_alignment))
    [ "$load_alignment_value" -ge 16384 ] ||
      fail "$abi 未满足 16 KiB ELF 对齐：PT_LOAD p_align=$load_alignment"
    load_count=$((load_count + 1))
  done <<EOF
$load_alignments
EOF
  [ "$load_count" -gt 0 ] || fail "$abi 未检测到 PT_LOAD"

  if "$LLVM_READELF" -d "$output_abi_dir/7zz" |
     grep -F "libc++_shared.so" >/dev/null; then
    fail "$abi 仍依赖未随模块提供的 libc++_shared.so"
  fi

  rm -f "$output_abi_dir/7zz.unstripped"
  printf '完成：%s\n' "$output_abi_dir/7zz"
}

case " $REQUESTED_ABIS " in
  *" arm64-v8a "*)
    build_abi "arm64-v8a" "aarch64-linux-android" "AArch64" "/system/bin/linker64"
    ;;
esac
case " $REQUESTED_ABIS " in
  *" armeabi-v7a "*)
    build_abi "armeabi-v7a" "armv7a-linux-androideabi" "ARM" "/system/bin/linker"
    ;;
esac
case " $REQUESTED_ABIS " in
  *" x86_64 "*)
    build_abi "x86_64" "x86_64-linux-android" "X86-64" "/system/bin/linker64"
    ;;
esac

cp -f "$SOURCE_DIR/DOC/License.txt" "$OUT_DIR/LICENSE-7ZIP.txt"
{
  for abi in arm64-v8a armeabi-v7a x86_64; do
    binary="$OUT_DIR/$abi/7zz"
    [ -f "$binary" ] || continue
    printf '%s  %s/7zz\n' "$(sha256_file "$binary")" "$abi"
  done
  printf '%s  LICENSE-7ZIP.txt\n' "$(sha256_file "$OUT_DIR/LICENSE-7ZIP.txt")"
} > "$OUT_DIR/SHA256SUMS"

printf '\n7-Zip Android 三架构构建完成：%s\n' "$OUT_DIR"
printf '请将各 ABI 的 7zz 放入 module/bin/<ABI>/7zz 后再打包模块。\n'
