# CPA 发布版本目录

本仓库是 **CPA 发布仓库**：这里保留可安装的 CPA CLI 版本、CPAMP 配套镜像和部署入口；功能源码分别维护在
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 与
[CPA-Manager-Pro](https://github.com/abc124774961/CPA-Manager-Pro)。版本以 Git tag、GHCR 镜像 manifest digest
和校验文件为准，客户部署请固定版本，不使用 `latest`。

机器可读清单（含各架构 digest）：[`release-catalog.json`](release-catalog.json)。

## 当前验证组合

| 组件 | 发布版本 | 发布 tag | 镜像 | manifest digest | 架构 |
| --- | --- | --- | --- | --- | --- |
| CPA CLI | `v7.2.148-cpa.3` | [`v7.2.148-cpa.3`](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases/tag/v7.2.148-cpa.3) | `ghcr.io/abc124774961/cli-proxy-api-cpa:v7.2.148-cpa.3` | `sha256:a7938c5a716127829bd3a7fe524b6fbdf8509f44d1a0c8304983bff642cd8bad` | `linux/amd64`, `linux/arm64` |
| CPAMP（Manager + Agent） | `v1.12.8-cpa.1` | `cpamp-pool-v1.12.8-cpa.1`（[发布模板](deploy/cpamp-pool-server/)） | `ghcr.io/abc124774961/cpa-manager-plus:v1.12.8-cpa.1` | `sha256:f1cde753aa222b77a11e387fbf73eeac106a0d78db236fd5f13bf31a0860352b` | `linux/amd64`, `linux/arm64` |

CPAMP 镜像同时包含 `cpa-manager-plus` 和 `cpamp-agent` 两个可执行文件；两者必须使用同一个版本 tag。

## CPA CLI 历史 tag

| tag | 状态 | 说明 |
| --- | --- | --- |
| [`v7.2.148-cpa.3`](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/tree/v7.2.148-cpa.3) | 当前 | 推荐安装版本；镜像 digest 见上表 |
| [`v7.2.148-cpa.2`](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/tree/v7.2.148-cpa.2) | 上一版本 | 镜像仍可按固定 digest 使用 |
| [`v7.2.148-cpa.1`](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/tree/v7.2.148-cpa.1) | 仅 tag | 早期发布 tag，安装前请确认对应镜像是否仍可拉取 |

## 校验和与发布资产

- CPA CLI 发布页会附带 `checksums.txt`、manifest 和版本归档：
  [v7.2.148-cpa.3 assets](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases/tag/v7.2.148-cpa.3)。
- 容器部署优先校验 GHCR manifest digest；需要架构级校验时，使用 `docker buildx imagetools inspect <image>`。
- CPAMP 的构建来源、双架构检查和 Manager/Agent 文件检查记录在
  [pool-image workflow](https://github.com/abc124774961/CPA-Manager-Pro/actions/runs/33803882344)。

## 部署入口

- [CPA CLI + CPAMP 统一部署](docs/deployment-cpa-cpamp.zh-CN.md)
- [CPA CLI 单体安装](README_CN.md#客户服务器部署docker-compose)
- [CPAMP 发布模板](deploy/cpamp-pool-server/README.md)
- [CPAMP 源码与完整模板](https://github.com/abc124774961/CPA-Manager-Pro/tree/main/deploy/pool-server)
- [授权与宽限期说明](README_CN.md#首次启动验证)

## 发布约定

1. CPA CLI 使用 `vMAJOR.MINOR.PATCH-cpa.N` tag；CPAMP pool bundle 使用 `cpamp-pool-vX.Y.Z-cpa.N` source tag 和
   `vX.Y.Z-cpa.N` 镜像 tag。
2. 每次发布必须记录 source commit、镜像 manifest digest、`linux/amd64`/`linux/arm64` 架构和可复现的校验文件。
3. 发布前运行 `scripts/verify-release-bundle.sh <CPA_TAG>`；不要把客户 `.env`、Secret、授权租约或加密插件包提交到仓库。
4. 商城授权地址固定为 `https://p.666ttt.net/api/storefront`；首次激活和到期宽限租约由商城签名，客户端本地兜底上限为 6 小时。
