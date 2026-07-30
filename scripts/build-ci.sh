#!/usr/bin/env sh
set -eu

export VERSION=${VERSION:-0.01}
export SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-1785340800}
exec sh "$(dirname "$0")/build.sh"
