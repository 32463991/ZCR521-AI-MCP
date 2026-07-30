#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$SCRIPT_DIR/SOURCE.lock"

CACHE_DIR=${SEVENZIP_CACHE_DIR:-"$SCRIPT_DIR/.cache"}
SOURCE_DIR=${1:-"$CACHE_DIR/7zip-$SEVENZIP_VERSION"}
DOWNLOAD_DIR="$CACHE_DIR/downloads"
ARCHIVE_PATH="$DOWNLOAD_DIR/$SEVENZIP_ARCHIVE"
MARKER_PATH="$SOURCE_DIR/.zcr-source.sha256"

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
    fail "缺少 sha256sum、shasum 或 openssl，无法验证上游源码"
  fi
}

download_archive() {
  destination=$1
  temporary="$destination.tmp.$$"
  rm -f "$temporary"

  if command -v curl >/dev/null 2>&1; then
    curl --fail --location --proto '=https' --tlsv1.2 \
      --output "$temporary" "$SEVENZIP_URL"
  elif command -v wget >/dev/null 2>&1; then
    wget --https-only --output-document="$temporary" "$SEVENZIP_URL"
  else
    fail "缺少 curl 或 wget，无法下载 7-Zip 源码"
  fi

  actual=$(sha256_file "$temporary")
  [ "$actual" = "$SEVENZIP_SHA256" ] ||
    fail "源码 SHA-256 不匹配：期望 $SEVENZIP_SHA256，实际 $actual"
  mv -f "$temporary" "$destination"
}

case "$SOURCE_DIR" in
  ''|/)
    fail "拒绝使用不安全的源码目录"
    ;;
esac

if [ -r "$MARKER_PATH" ]; then
  marker=$(sed -n '1p' "$MARKER_PATH")
  if [ "$marker" = "$SEVENZIP_SHA256" ] &&
     [ -f "$SOURCE_DIR/CPP/7zip/Bundles/Alone2/makefile.gcc" ] &&
     [ -f "$SOURCE_DIR/DOC/License.txt" ]; then
    printf '%s\n' "$SOURCE_DIR"
    exit 0
  fi
  fail "现有源码目录的版本标记无效；请人工检查后删除：$SOURCE_DIR"
fi

mkdir -p "$DOWNLOAD_DIR"
if [ -f "$ARCHIVE_PATH" ]; then
  actual=$(sha256_file "$ARCHIVE_PATH")
  if [ "$actual" != "$SEVENZIP_SHA256" ]; then
    rm -f "$ARCHIVE_PATH"
  fi
fi
[ -f "$ARCHIVE_PATH" ] || download_archive "$ARCHIVE_PATH"

actual=$(sha256_file "$ARCHIVE_PATH")
[ "$actual" = "$SEVENZIP_SHA256" ] ||
  fail "缓存源码 SHA-256 不匹配：期望 $SEVENZIP_SHA256，实际 $actual"

temporary_source="$SOURCE_DIR.tmp.$$"
case "$temporary_source" in
  "$SOURCE_DIR.tmp."*)
    rm -rf "$temporary_source"
    ;;
  *)
    fail "临时源码目录校验失败"
    ;;
esac
mkdir -p "$temporary_source"
trap 'rm -rf "$temporary_source"' EXIT HUP INT TERM

tar -xJf "$ARCHIVE_PATH" -C "$temporary_source" ||
  fail "无法解压 $SEVENZIP_ARCHIVE；主机 tar 必须支持 xz"
[ -f "$temporary_source/CPP/7zip/Bundles/Alone2/makefile.gcc" ] ||
  fail "上游源码结构不符合 7-Zip 26.01"
[ -f "$temporary_source/DOC/License.txt" ] ||
  fail "上游源码缺少 DOC/License.txt"

printf '%s\n' "$SEVENZIP_SHA256" > "$temporary_source/.zcr-source.sha256"
mkdir -p "$(dirname -- "$SOURCE_DIR")"
[ ! -e "$SOURCE_DIR" ] ||
  fail "目标源码目录已存在且未经验证：$SOURCE_DIR"
mv "$temporary_source" "$SOURCE_DIR"
trap - EXIT HUP INT TERM

printf '%s\n' "$SOURCE_DIR"
