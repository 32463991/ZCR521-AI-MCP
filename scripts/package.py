#!/usr/bin/env python3
"""Create deterministic module and desktop bridge release artifacts."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import pathlib
import re
import shutil
import zipfile


ABIS = ("arm64-v8a", "armeabi-v7a", "x86_64")


def digest(path: pathlib.Path) -> str:
    sha = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            sha.update(chunk)
    return sha.hexdigest()


def normalized_mode(relative: str) -> int:
    name = pathlib.PurePosixPath(relative)
    if (
        relative.endswith(".sh")
        or str(name).startswith("bin/")
        or relative.endswith("update-binary")
    ):
        return 0o755
    return 0o644


def write_zip_entries(
    entries: list[tuple[str, pathlib.Path]],
    destination: pathlib.Path,
    epoch: int,
) -> None:
    timestamp = dt.datetime.fromtimestamp(max(epoch, 315532800), dt.timezone.utc)
    zip_time = (timestamp.year, timestamp.month, timestamp.day, timestamp.hour, timestamp.minute, timestamp.second)
    with zipfile.ZipFile(destination, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        for relative, path in sorted(entries):
            info = zipfile.ZipInfo(relative, zip_time)
            info.create_system = 3
            info.external_attr = (normalized_mode(relative) & 0xFFFF) << 16
            info.compress_type = zipfile.ZIP_DEFLATED
            with path.open("rb") as content:
                with archive.open(info, "w", force_zip64=True) as target:
                    shutil.copyfileobj(content, target, length=1024 * 1024)


def deterministic_zip(source: pathlib.Path, destination: pathlib.Path, epoch: int) -> None:
    entries = [
        (path.relative_to(source).as_posix(), path)
        for path in source.rglob("*")
        if path.is_file()
    ]
    write_zip_entries(entries, destination, epoch)


def source_entries(repo: pathlib.Path, prefix: str) -> list[tuple[str, pathlib.Path]]:
    excluded_roots = {".git", ".tools", "build", "dist"}
    entries: list[tuple[str, pathlib.Path]] = []
    for path in repo.rglob("*"):
        if not path.is_file():
            continue
        relative = path.relative_to(repo)
        parts = relative.parts
        if not parts or parts[0] in excluded_roots:
            continue
        if parts[:3] in {
            ("third_party", "7zip", ".cache"),
            ("third_party", "7zip", "build"),
            ("third_party", "7zip", "out"),
        }:
            continue
        if parts[:2] in {("scripts", "__pycache__"), ("module", "bin")}:
            continue
        if path.suffix in {".pyc", ".tmp", ".log"}:
            continue
        entries.append((f"{prefix}/{relative.as_posix()}", path))
    return entries


def documentation_entries(repo: pathlib.Path, prefix: str) -> list[tuple[str, pathlib.Path]]:
    candidates = [
        repo / "README.md",
        repo / "CHANGELOG.md",
        repo / "LICENSE",
        repo / "THIRD_PARTY_NOTICES.md",
        repo / "schemas" / "tools.json",
    ]
    candidates.extend(sorted((repo / "docs").glob("*.md")))
    candidates.extend(sorted((repo / "scripts" / "usb").glob("*")))
    return [
        (f"{prefix}/{path.relative_to(repo).as_posix()}", path)
        for path in candidates
        if path.is_file()
    ]


def go_module_packages(repo: pathlib.Path) -> list[dict[str, object]]:
    modules = repo / "vendor" / "modules.txt"
    if not modules.is_file():
        raise SystemExit("vendor/modules.txt 不存在，无法生成完整依赖 SBOM")
    packages: list[dict[str, object]] = []
    seen: set[tuple[str, str]] = set()
    for line in modules.read_text(encoding="utf-8").splitlines():
        if not line.startswith("# ") or line.startswith("## "):
            continue
        fields = line[2:].split()
        if len(fields) < 2 or not fields[1].startswith("v"):
            continue
        name, version = fields[:2]
        key = (name, version)
        if key in seen:
            continue
        seen.add(key)
        identifier = hashlib.sha256(f"{name}@{version}".encode()).hexdigest()[:16]
        license_id = (
            "Apache-2.0 AND MIT"
            if name == "github.com/modelcontextprotocol/go-sdk"
            else "NOASSERTION"
        )
        package: dict[str, object] = {
            "SPDXID": f"SPDXRef-GoModule-{identifier}",
            "name": name,
            "versionInfo": version,
            "downloadLocation": f"https://{name}",
            "licenseConcluded": license_id,
            "licenseDeclared": license_id,
            "copyrightText": "NOASSERTION",
            "externalRefs": [
                {
                    "referenceCategory": "PACKAGE-MANAGER",
                    "referenceType": "purl",
                    "referenceLocator": f"pkg:golang/{name}@{version}",
                }
            ],
        }
        if name == "github.com/modelcontextprotocol/go-sdk":
            package["comment"] = "Pinned release v1.7.0-pre.3, upstream commit 827f90b."
        packages.append(package)
    return packages


def write_sbom(repo: pathlib.Path, dist: pathlib.Path, version: str, epoch: int) -> pathlib.Path:
    created = dt.datetime.fromtimestamp(epoch, dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    packages = [
        {
            "SPDXID": "SPDXRef-Package-ZCR521",
            "name": "ZCR521-Android-AI-MCP",
            "versionInfo": version,
            "downloadLocation": "NOASSERTION",
            "licenseConcluded": "GPL-3.0-or-later",
            "licenseDeclared": "GPL-3.0-or-later",
            "copyrightText": "NOASSERTION",
        },
        {
            "SPDXID": "SPDXRef-Package-7Zip",
            "name": "7-Zip",
            "versionInfo": "26.01",
            "downloadLocation": "https://www.7-zip.org/",
            "licenseConcluded": "LicenseRef-7Zip",
            "licenseDeclared": "LicenseRef-7Zip",
            "copyrightText": "Copyright Igor Pavlov and contributors",
        },
    ]
    packages.extend(go_module_packages(repo))
    for package in packages:
        package["filesAnalyzed"] = False
    document = {
        "spdxVersion": "SPDX-2.3",
        "dataLicense": "CC0-1.0",
        "SPDXID": "SPDXRef-DOCUMENT",
        "name": f"ZCR521-Android-AI-MCP-{version}",
        "documentNamespace": f"https://zcr521.local/sbom/{version}/{epoch}",
        "creationInfo": {"created": created, "creators": ["Tool: ZCR521 package.py"]},
        "hasExtractedLicensingInfos": [
            {
                "licenseId": "LicenseRef-7Zip",
                "name": "7-Zip combined license",
                "extractedText": "See third_party/7zip/LICENSE-7ZIP.txt in the corresponding source release.",
                "seeAlsos": ["https://www.7-zip.org/license.txt"],
            }
        ],
        "packages": packages,
        "relationships": [
            {
                "spdxElementId": "SPDXRef-DOCUMENT",
                "relationshipType": "DESCRIBES",
                "relatedSpdxElement": "SPDXRef-Package-ZCR521",
            }
        ]
        + [
            {
                "spdxElementId": "SPDXRef-Package-ZCR521",
                "relationshipType": "DEPENDS_ON",
                "relatedSpdxElement": item["SPDXID"],
            }
            for item in packages[1:]
        ],
    }
    output = dist / f"ZCR521-Android-AI-MCP-v{version}.spdx.json"
    output.write_text(json.dumps(document, ensure_ascii=False, indent=2) + "\n", encoding="utf-8", newline="\n")
    return output


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", required=True)
    parser.add_argument("--module-stage", required=True)
    parser.add_argument("--bridge-dir", required=True)
    parser.add_argument("--dist", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--epoch", required=True, type=int)
    args = parser.parse_args()

    repo = pathlib.Path(args.repo).resolve()
    module = pathlib.Path(args.module_stage).resolve()
    bridges = pathlib.Path(args.bridge_dir).resolve()
    dist = pathlib.Path(args.dist).resolve()
    dist.mkdir(parents=True, exist_ok=True)

    release_prefix = f"ZCR521-Android-AI-MCP-v{args.version}"
    cleanup_patterns = (
        re.compile(r"^ZCR521-Android-AI-MCP-v.*\.(?:zip|json)$"),
        re.compile(r"^zcr521-bridge-"),
        re.compile(r"^(?:tools\.json|SHA256SUMS)$"),
    )
    for path in dist.iterdir():
        if path.is_file() and any(pattern.search(path.name) for pattern in cleanup_patterns):
            path.unlink()

    for abi in ABIS:
        for binary in ("zcr521d", "7zz"):
            candidate = module / "bin" / abi / binary
            if not candidate.is_file() or candidate.stat().st_size == 0:
                raise SystemExit(f"缺少真实构建产物: {candidate}")
            if candidate.read_bytes()[:4] != b"\x7fELF":
                raise SystemExit(f"Android 产物不是 ELF: {candidate}")

    module_zip = dist / f"{release_prefix}-universal.zip"
    deterministic_zip(module, module_zip, args.epoch)

    documentation_zip = dist / f"{release_prefix}-documentation.zip"
    write_zip_entries(
        documentation_entries(repo, release_prefix),
        documentation_zip,
        args.epoch,
    )

    source_archive_entries = source_entries(repo, release_prefix)
    source_zip = dist / f"{release_prefix}-source.zip"
    write_zip_entries(
        source_archive_entries,
        source_zip,
        args.epoch,
    )
    template_zip = dist / f"{release_prefix}-template.zip"
    write_zip_entries(
        source_archive_entries,
        template_zip,
        args.epoch,
    )

    bridge_names = (
        "zcr521-bridge-windows-amd64.exe",
        "zcr521-bridge-linux-amd64",
        "zcr521-bridge-linux-arm64",
        "zcr521-bridge-macos-amd64",
        "zcr521-bridge-macos-arm64",
    )
    for name in bridge_names:
        source = bridges / name
        if not source.is_file() or source.stat().st_size == 0:
            raise SystemExit(f"缺少 bridge: {source}")
        shutil.copy2(source, dist / name)

    schema = repo / "schemas" / "tools.json"
    if schema.is_file():
        shutil.copy2(schema, dist / "tools.json")
    sbom = write_sbom(repo, dist, args.version, args.epoch)

    artifact_paths = [
        module_zip,
        documentation_zip,
        source_zip,
        template_zip,
        *(dist / name for name in bridge_names),
        dist / "tools.json",
        sbom,
    ]
    checksums = []
    for path in sorted(artifact_paths):
        if not path.is_file():
            raise SystemExit(f"发布产物不存在: {path}")
        checksums.append(f"{digest(path)}  {path.name}")
    (dist / "SHA256SUMS").write_text("\n".join(checksums) + "\n", encoding="ascii", newline="\n")
    print(module_zip)


if __name__ == "__main__":
    main()
