#!/usr/bin/env python3
"""Create the deterministic flashable module ZIP."""

from __future__ import annotations

import argparse
import datetime as dt
import pathlib
import re
import shutil
import zipfile


ABIS = ("arm64-v8a", "armeabi-v7a", "x86_64")


def normalized_mode(relative: str) -> int:
    name = pathlib.PurePosixPath(relative)
    if (
        relative.endswith(".sh")
        or str(name).startswith("bin/")
        or relative.endswith("update-binary")
    ):
        return 0o755
    return 0o644


def deterministic_zip(source: pathlib.Path, destination: pathlib.Path, epoch: int) -> None:
    timestamp = dt.datetime.fromtimestamp(max(epoch, 315532800), dt.timezone.utc)
    zip_time = (
        timestamp.year,
        timestamp.month,
        timestamp.day,
        timestamp.hour,
        timestamp.minute,
        timestamp.second,
    )
    entries = sorted(
        (path.relative_to(source).as_posix(), path)
        for path in source.rglob("*")
        if path.is_file()
    )
    with zipfile.ZipFile(
        destination,
        "w",
        compression=zipfile.ZIP_DEFLATED,
        compresslevel=9,
    ) as archive:
        for relative, path in entries:
            info = zipfile.ZipInfo(relative, zip_time)
            info.create_system = 3
            info.external_attr = (normalized_mode(relative) & 0xFFFF) << 16
            info.compress_type = zipfile.ZIP_DEFLATED
            with path.open("rb") as content:
                with archive.open(info, "w", force_zip64=True) as target:
                    shutil.copyfileobj(content, target, length=1024 * 1024)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--module-stage", required=True)
    parser.add_argument("--dist", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--epoch", required=True, type=int)
    args = parser.parse_args()

    module = pathlib.Path(args.module_stage).resolve()
    dist = pathlib.Path(args.dist).resolve()
    dist.mkdir(parents=True, exist_ok=True)

    if any(path.is_file() for path in (module / "licenses").rglob("*")):
        raise SystemExit("模块源码仍包含 licenses 子目录")

    for abi in ABIS:
        for binary in ("zcr521d", "7zz"):
            candidate = module / "bin" / abi / binary
            if not candidate.is_file() or candidate.stat().st_size == 0:
                raise SystemExit(f"缺少真实构建产物: {candidate}")
            if candidate.read_bytes()[:4] != b"\x7fELF":
                raise SystemExit(f"Android 产物不是 ELF: {candidate}")

    cleanup_patterns = (
        re.compile(r"^ZCR521-Android-AI-MCP-v.*\.(?:zip|json)$"),
        re.compile(r"^zcr521-bridge-"),
        re.compile(r"^(?:tools\.json|SHA256SUMS)$"),
    )
    for path in dist.iterdir():
        if path.is_file() and any(pattern.search(path.name) for pattern in cleanup_patterns):
            path.unlink()

    module_zip = dist / f"ZCR521-Android-AI-MCP-v{args.version}-universal.zip"
    deterministic_zip(module, module_zip, args.epoch)
    print(module_zip)


if __name__ == "__main__":
    main()
