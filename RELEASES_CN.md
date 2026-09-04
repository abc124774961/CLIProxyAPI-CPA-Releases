# CPA 发布版本目录

本页只记录已发布的 CPA CLI、CPAMP 版本和部署入口。普通用户只需本公开仓库中的发布镜像和模板，
不需要访问私有源码仓库。完整参数与功能说明引用独立源码仓库；部署请固定版本 tag 或镜像 digest，
不使用 `latest`。

## 当前验证组合

统一组合发布 tag：`v7.2.148-cpa.5`。该 tag 是 CPA CLI 与 CPAMP 客户部署包的统一配套锚点。

| 组件 | 组件版本/tag | 镜像 | 组合发布 tag | 架构 |
| --- | --- | --- | --- | --- |
| CPA CLI | `v7.2.148-cpa.4` | `ghcr.io/abc124774961/cli-proxy-api-cpa:v7.2.148-cpa.4` | `v7.2.148-cpa.5` | `linux/amd64`、`linux/arm64` |
| CPAMP（Manager + Agent） | `v1.12.8-cpa.2` | `ghcr.io/abc124774961/cpa-manager-plus:v1.12.8-cpa.2` | `v7.2.148-cpa.5` | `linux/amd64`、`linux/arm64` |

对应镜像 manifest digest、平台 digest、源码提交和校验文件见 [release-catalog.json](release-catalog.json) 与
[release-manifest.json](release-manifest.json)。CPAMP 镜像同时包含 `cpa-manager-plus` 和 `cpamp-agent`。

## 已发布 CPA CLI

| 组件版本 | 组合发布 tag | 状态 | 发布页 |
| --- | --- | --- | --- |
| `v7.2.148-cpa.4` | `v7.2.148-cpa.5` | 当前 | [Release](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases/tag/v7.2.148-cpa.5) |
| `v7.2.148-cpa.3` | `v7.2.148-cpa.4` | 上一版本 | [Release](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases/tag/v7.2.148-cpa.4) |
| `v7.2.148-cpa.2` | 上上版本 | [Release](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases/tag/v7.2.148-cpa.2) |
| `v7.2.148-cpa.1` | 早期版本 | [Tag](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/tree/v7.2.148-cpa.1) |

## 已发布 CPAMP

| 组件版本 | 组合发布 tag | 状态 | 发布页 |
| --- | --- | --- | --- |
| `v1.12.8-cpa.2` | `v7.2.148-cpa.5` | 当前 | [Release](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases/tag/v7.2.148-cpa.5) |
| `v1.12.8-cpa.1` | `v7.2.148-cpa.4` | 上一版本 | [Release](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases/tag/v7.2.148-cpa.4) |

## 部署入口

- [CPA CLI + CPAMP 统一部署](docs/deployment-cpa-cpamp.zh-CN.md)
- [CPAMP 发布模板](deploy/cpamp-pool-server/README.md)
- [GitHub Releases](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases)
- [CLIProxyAPI 源码与完整说明](https://github.com/router-for-me/CLIProxyAPI)
- [CPA-Manager-Pro 源码与完整 pool-server 模板（仅供维护者参考）](https://github.com/abc124774961/CPA-Manager-Pro/tree/main/deploy/pool-server)

商城授权地址：`https://p.666ttt.net/api/storefront`。商城签名租约决定正式授权和到期宽限；本地网络故障兜底上限为 6 小时。
