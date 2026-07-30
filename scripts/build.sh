#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VERSION=${VERSION:-0.01}
SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-1785340800}
NDK=${ANDROID_NDK_HOME:-${ANDROID_NDK_ROOT:-}}
GO_BIN=${GO_BIN:-go}
PYTHON=${PYTHON:-python3}
SKIP_TESTS=${SKIP_TESTS:-0}
SKIP_7ZIP=${SKIP_7ZIP:-0}

if [ -z "$NDK" ] || [ ! -d "$NDK" ]; then
  printf '%s\n' "ANDROID_NDK_HOME 必须指向 NDK r29。" >&2
  exit 2
fi
if [ "$($GO_BIN env GOVERSION)" != "go1.26.5" ]; then
  printf '%s\n' "构建必须使用 Go 1.26.5。" >&2
  exit 2
fi
if [ ! -f "$NDK/source.properties" ] ||
   ! grep -Eq '^[[:space:]]*Pkg\.Revision[[:space:]]*=[[:space:]]*29\.0\.14206865[[:space:]]*$' "$NDK/source.properties"; then
  printf '%s\n' "Android NDK 必须精确为 r29 (29.0.14206865)。" >&2
  exit 2
fi

cd "$ROOT"
export SOURCE_DATE_EPOCH GOTOOLCHAIN=local
"$GO_BIN" mod verify
if [ "$SKIP_TESTS" != "1" ]; then
  "$GO_BIN" test ./...
  "$GO_BIN" test -race ./...
fi
"$GO_BIN" mod vendor
"$GO_BIN" run -mod=vendor ./cmd/zcr521d schema --output "$ROOT/schemas/tools.json"

COMMIT=unknown
if command -v git >/dev/null 2>&1 && git rev-parse --verify HEAD >/dev/null 2>&1; then
  COMMIT=$(git rev-parse HEAD)
fi
BUILD_TIME=$("$PYTHON" -c 'import datetime,os; print(datetime.datetime.fromtimestamp(int(os.environ["SOURCE_DATE_EPOCH"]),datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))')
MODULE_PROP_SHA256=$("$PYTHON" -c 'import hashlib,pathlib,sys; print(hashlib.sha256(pathlib.Path(sys.argv[1]).read_bytes()).hexdigest())' "$ROOT/module/module.prop")
LD_COMMON="-s -w -buildid= -X github.com/zcr521/android-ai-mcp/internal/buildinfo.Version=$VERSION -X github.com/zcr521/android-ai-mcp/internal/buildinfo.Commit=$COMMIT -X github.com/zcr521/android-ai-mcp/internal/buildinfo.BuildTime=$BUILD_TIME -X github.com/zcr521/android-ai-mcp/internal/buildinfo.ModulePropSHA256=$MODULE_PROP_SHA256"

case "$(uname -s)" in
  Darwin) HOST_TAG=darwin-x86_64 ;;
  Linux) HOST_TAG=linux-x86_64 ;;
  *) printf '%s\n' "不支持的构建主机；Windows 请运行 build.ps1。" >&2; exit 2 ;;
esac
TOOLCHAIN="$NDK/toolchains/llvm/prebuilt/$HOST_TAG/bin"
mkdir -p build/android build/bridge dist

build_android() {
  abi=$1
  arch=$2
  cc=$3
  goarm=${4:-}
  mkdir -p "build/android/$abi"
  GOOS=android GOARCH="$arch" GOARM="$goarm" CGO_ENABLED=1 CC="$TOOLCHAIN/$cc" \
    "$GO_BIN" build -mod=vendor -trimpath -buildmode=pie -tags osusergo,netgo \
    -ldflags "$LD_COMMON -linkmode external -extldflags=-Wl,-z,max-page-size=16384" \
    -o "build/android/$abi/zcr521d" ./cmd/zcr521d
}
build_android arm64-v8a arm64 aarch64-linux-android26-clang
build_android armeabi-v7a arm armv7a-linux-androideabi26-clang 7
build_android x86_64 amd64 x86_64-linux-android26-clang

"$PYTHON" scripts/verify_elf.py --file build/android/arm64-v8a/zcr521d --machine 183 --page-size 16384 --api 26
"$PYTHON" scripts/verify_elf.py --file build/android/armeabi-v7a/zcr521d --machine 40 --page-size 16384 --api 26
"$PYTHON" scripts/verify_elf.py --file build/android/x86_64/zcr521d --machine 62 --page-size 16384 --api 26

if [ "$SKIP_7ZIP" != "1" ]; then
  ANDROID_NDK_HOME="$NDK" ANDROID_API_LEVEL=26 \
    SEVENZIP_OUT_DIR="$ROOT/third_party/7zip/out" \
    third_party/7zip/build-android.sh
fi

build_bridge() {
  host_os=$1
  host_arch=$2
  name=$3
  GOOS="$host_os" GOARCH="$host_arch" CGO_ENABLED=0 \
    "$GO_BIN" build -mod=vendor -trimpath -ldflags "$LD_COMMON" -o "build/bridge/$name" ./cmd/zcr521-bridge
}
build_bridge windows amd64 zcr521-bridge-windows-amd64.exe
build_bridge linux amd64 zcr521-bridge-linux-amd64
build_bridge linux arm64 zcr521-bridge-linux-arm64
build_bridge darwin amd64 zcr521-bridge-macos-amd64
build_bridge darwin arm64 zcr521-bridge-macos-arm64

rm -rf build/module
cp -R module build/module
for abi in arm64-v8a armeabi-v7a x86_64; do
  mkdir -p "build/module/bin/$abi"
  cp "build/android/$abi/zcr521d" "build/module/bin/$abi/zcr521d"
  cp "third_party/7zip/out/$abi/7zz" "build/module/bin/$abi/7zz"
done
mkdir -p build/module/licenses
cp third_party/7zip/out/LICENSE-7ZIP.txt build/module/licenses/7zip.txt
cp LICENSE build/module/licenses/GPL-3.0-or-later.txt
cp THIRD_PARTY_NOTICES.md build/module/licenses/THIRD_PARTY_NOTICES.md

"$PYTHON" scripts/package.py --repo "$ROOT" --module-stage "$ROOT/build/module" --bridge-dir "$ROOT/build/bridge" --dist "$ROOT/dist" --version "$VERSION" --epoch "$SOURCE_DATE_EPOCH"
"$PYTHON" scripts/verify_module.py "$ROOT/dist/ZCR521-Android-AI-MCP-v$VERSION-universal.zip"
