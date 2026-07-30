#!/usr/bin/env python3
"""Static universal Root module acceptance checks."""

from __future__ import annotations

import argparse
import hashlib
import io
import pathlib
import stat
import zipfile


ABIS = ("arm64-v8a", "armeabi-v7a", "x86_64")
REQUIRED = (
    "module.prop",
    "customize.sh",
    "service.sh",
    "action.sh",
    "uninstall.sh",
    "META-INF/com/google/android/update-binary",
    "META-INF/com/google/android/updater-script",
)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("zip")
    args = parser.parse_args()
    path = pathlib.Path(args.zip)
    if not path.is_file():
        raise SystemExit(f"ZIP 不存在: {path}")
    with zipfile.ZipFile(path) as archive:
        names = set(archive.namelist())
        for name in REQUIRED:
            if name not in names:
                raise SystemExit(f"ZIP 缺少 {name}")
        prop_data = archive.read("module.prop")
        prop_digest = hashlib.sha256(prop_data).hexdigest().encode("ascii")
        for abi in ABIS:
            for binary in ("zcr521d", "7zz"):
                name = f"bin/{abi}/{binary}"
                if name not in names:
                    raise SystemExit(f"ZIP 缺少 {name}")
                data = archive.read(name)
                if len(data) == 0 or data[:4] != b"\x7fELF":
                    raise SystemExit(f"{name} 不是非空 ELF")
                mode = archive.getinfo(name).external_attr >> 16
                if mode & stat.S_IXUSR == 0:
                    raise SystemExit(f"{name} 没有执行位")
                if binary == "zcr521d" and prop_digest not in data:
                    raise SystemExit(f"{name} 未内置 module.prop SHA-256")
        for name in names:
            if name.endswith(".sh") or name.endswith("update-binary") or name.endswith("updater-script"):
                script_data = archive.read(name)
                if b"\r\n" in script_data:
                    raise SystemExit(f"{name} 不是 LF 换行")
                for index, line in enumerate(script_data.decode("utf-8").splitlines()):
                    stripped = line.lstrip()
                    if not stripped.startswith("#"):
                        continue
                    if index == 0 and (stripped.startswith("#!") or stripped == "#MAGISK"):
                        continue
                    raise SystemExit(f"{name} 仍含注释: {line}")
        prop = prop_data.decode("utf-8")
        expected_prop = (
            "id=zcr521.android.mcp\n"
            "name=ZCR521 AI MCP\n"
            "version=0.01\n"
            "versionCode=1\n"
            "author=小骨@Xiaogu_zcr521\n"
            "description=通用 root MCP扩展 （让AI管理手机）\n"
        )
        if prop != expected_prop:
            raise SystemExit("module.prop 元数据与指定内容不一致")
        action = archive.read("action.sh").decode("utf-8")
        for forbidden in ("音量加", "音量减", "查看 MCP 运行状态", "启动服务", "恢复默认配置"):
            if forbidden in action:
                raise SystemExit("action.sh 仍含旧操作菜单")
        if "作者:小骨@Xiaogu_zcr521" not in action or "当前版本: 0.01" not in action:
            raise SystemExit("action.sh 未显示指定作者或版本")
        if "zcr_print_summary" not in action:
            raise SystemExit("action.sh 未保留运行摘要")
        common = archive.read("common.sh").decode("utf-8")
        if "/data/adb/zcr521-mcp/MCP地址.txt" not in common:
            raise SystemExit("未生成指定 MCP 地址文件")
        if "局域网地址：当前未获取" in common:
            raise SystemExit("地址文件仍会写入未获取占位值")
        for child in ("downloads", "uploads", "scripts", "projects", "output", "backups", "packages", "modules"):
            if f'"$ZCR_WORK_DIR/{child}"' in common:
                raise SystemExit(f"安装脚本仍会预创建工作区子目录: {child}")
        customize = archive.read("customize.sh").decode("utf-8")
        if "tg://resolve?domain=Xiaogu_zcr521" not in customize:
            raise SystemExit("安装脚本未尝试跳转官方 TG")
        if "sepolicy.rule" in names:
            raise SystemExit("模块仍包含仅注释的 sepolicy.rule")
        if any(name == "licenses/" or name.startswith("licenses/") for name in names):
            raise SystemExit("模块仍包含 licenses 子目录")
        if "post-fs-data.sh" in names:
            script = archive.read("post-fs-data.sh").decode("utf-8")
            forbidden = ("sleep 30", "zcr521d supervisor", "setenforce 0")
            if any(item in script for item in forbidden):
                raise SystemExit("post-fs-data.sh 含阻塞启动或禁用 SELinux")
    print(f"OK {path}")


if __name__ == "__main__":
    main()
