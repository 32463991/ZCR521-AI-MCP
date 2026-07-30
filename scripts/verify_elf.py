#!/usr/bin/env python3
"""Validate Android ELF identity, PIE type and 16 KiB LOAD alignment."""

from __future__ import annotations

import argparse
import os
import struct
import sys


def fail(message: str) -> None:
    raise SystemExit(f"ELF 验证失败: {message}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--file", required=True)
    parser.add_argument("--machine", required=True, type=int)
    parser.add_argument("--page-size", required=True, type=int)
    parser.add_argument("--api", required=True, type=int)
    args = parser.parse_args()

    if args.api != 26:
        fail(f"构建基线必须为 API 26，实际 {args.api}")
    if not os.path.isfile(args.file) or os.path.getsize(args.file) == 0:
        fail(f"文件不存在或为空: {args.file}")
    with open(args.file, "rb") as source:
        data = source.read()
    if data[:4] != b"\x7fELF":
        fail("不是 ELF")
    elf_class = data[4]
    endian = data[5]
    if elf_class not in (1, 2) or endian not in (1, 2):
        fail("ELF class/endian 无效")
    order = "<" if endian == 1 else ">"
    e_type, e_machine = struct.unpack_from(order + "HH", data, 16)
    if e_type != 3:
        fail(f"不是 ET_DYN PIE，e_type={e_type}")
    if e_machine != args.machine:
        fail(f"e_machine={e_machine}，期望 {args.machine}")

    if elf_class == 2:
        phoff = struct.unpack_from(order + "Q", data, 32)[0]
        phentsize, phnum = struct.unpack_from(order + "HH", data, 54)
        fmt = order + "IIQQQQQQ"
    else:
        phoff = struct.unpack_from(order + "I", data, 28)[0]
        phentsize, phnum = struct.unpack_from(order + "HH", data, 42)
        fmt = order + "IIIIIIII"
    expected = struct.calcsize(fmt)
    if phentsize < expected:
        fail("program header 尺寸无效")
    load_count = 0
    for index in range(phnum):
        start = phoff + index * phentsize
        fields = struct.unpack_from(fmt, data, start)
        if fields[0] != 1:
            continue
        load_count += 1
        if elf_class == 2:
            _, _, offset, vaddr, _, _, _, alignment = fields
        else:
            _, offset, vaddr, _, _, _, _, alignment = fields
        if alignment < args.page_size:
            fail(f"LOAD[{index}] p_align={alignment} 小于 {args.page_size}")
        if offset % args.page_size != vaddr % args.page_size:
            fail(f"LOAD[{index}] offset/vaddr 不满足 {args.page_size} 同余")
    if load_count == 0:
        fail("没有 PT_LOAD 段")
    print(f"OK {args.file}: machine={e_machine}, PIE, LOAD={load_count}, page={args.page_size}, api={args.api}")


if __name__ == "__main__":
    try:
        main()
    except (OSError, struct.error) as error:
        fail(str(error))
