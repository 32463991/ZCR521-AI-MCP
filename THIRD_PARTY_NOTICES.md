# 第三方软件声明

发布包包含或依赖以下主要组件。完整 Go 依赖清单同时写入 SPDX SBOM。

## Model Context Protocol Go SDK

- 模块：`github.com/modelcontextprotocol/go-sdk`
- 固定版本：`v1.7.0-pre.3`
- 对应提交：`827f90b`
- 许可证：处于 MIT → Apache-2.0 迁移期；本固定版本同时包含 Apache-2.0 与尚未重许可的 MIT 贡献，SPDX 表达式记为 `Apache-2.0 AND MIT`
- 上游：<https://github.com/modelcontextprotocol/go-sdk>

## 7-Zip

- 版本：26.01
- 固定源码包：`7z2601-src.tar.xz`
- SHA-256：`b2389e0e930b2f9a348cf0fe7d9870a46482a8ec044ee0bdf42e2136db31c3d6`
- 上游：<https://github.com/ip7z/7zip/releases/tag/26.01>

7-Zip 不是单一许可证：多数源码为 GNU LGPL v2.1 或更高版本；RAR 解码相关源码同时受 unRAR 限制；部分 LZFSE、Zstandard、XXH64 源码使用 BSD 或 public-domain 条款。官方 `DOC/License.txt` 已原样放入 `third_party/7zip/LICENSE-7ZIP.txt`，构建后同时放进模块 `licenses/7zip.txt`。该文件不得从发布包删除。

## Go 扩展库

- `golang.org/x/sys`：BSD-3-Clause
- `github.com/ulikunitz/xz`：BSD-3-Clause
- MCP SDK 的传递依赖：以 `go.sum`、`vendor/modules.txt` 与 SPDX SBOM 为准。

本项目自身使用 GPL-3.0-or-later；第三方组件继续适用各自许可证。
