# CLIProxyAPI CPA 发布版

本仓库只发布已经验证的 **CPA CLI** 与 **CPAMP（Manager + Agent）** 版本，并提供客户服务器部署入口。
功能源码、完整开发文档和变更记录分别维护在（仅供维护者参考）：

- [CLIProxyAPI 源码](https://github.com/router-for-me/CLIProxyAPI)
- [CPA-Manager-Pro 源码](https://github.com/abc124774961/CPA-Manager-Pro)

## 入口

- [当前版本目录](RELEASES_CN.md)
- [CPA CLI + CPAMP 中文部署流程](docs/deployment-cpa-cpamp.zh-CN.md)
- [CPAMP 发布模板](deploy/cpamp-pool-server/README.md)
- [CPAMP 轻量面板文件](management.html)
- [机器可读版本清单](release-catalog.json)
- [授权与运行时清单](release-manifest.json)

## 当前发布组合

本分支为公开 Beta，安装需要显式选择版本；既有稳定版和稳定下载入口保持原状。请先阅读 [Beta 安装说明](docs/release-beta-20260911.zh-CN.md)。

本次公开组合发布 tag 为 `v7.2.148-cpa.7-beta.1`。该 tag 是 CPA CLI 与 CPAMP 客户部署包的统一发布锚点；组件自身保持已验证的版本和镜像 tag，不会因为重新打包而伪造组件版本。 `release-manifest.json` 与 `release-catalog.json` 中的 `release_tag` 字段均指向该组合 tag。

| 组件 | 组件版本 / 镜像 tag | 固定镜像 | 组合发布 tag | 架构 |
| --- | --- | --- | --- | --- |
| CPA CLI | `v7.2.148-cpa.7-beta.1` | `ghcr.io/abc124774961/cli-proxy-api-cpa:v7.2.148-cpa.7-beta.1` | `v7.2.148-cpa.7-beta.1` | `linux/amd64`、`linux/arm64` |
| CPAMP（Manager + Agent） | `v1.12.10-cpa.1-beta.1` | `ghcr.io/abc124774961/cpa-manager-plus:v1.12.10-cpa.1-beta.1` | `v7.2.148-cpa.7-beta.1` | `linux/amd64`、`linux/arm64` |

部署时固定组合发布 tag `v7.2.148-cpa.7-beta.1`，或使用 `release-catalog.json` 中对应组件的 digest；不使用 `latest`。

## 客户服务器部署（Docker Compose）

普通用户**不需要访问 CPA-Manager-Pro 私有源码仓库**，也不需要 clone 私有代码。本公开仓库已经包含
CPA CLI/CPAMP 的固定镜像、Compose 文件、安装脚本和配置模板；按统一发布 tag `v7.2.148-cpa.7-beta.1` 获取配套文件，完整命令请按
[中文部署流程](docs/deployment-cpa-cpamp.zh-CN.md) 执行。流程摘要：

1. 在客户服务器准备 Docker Engine、Compose v2，并固定 CPA/CPAMP 版本。
2. CPA CLI 从本仓库 checkout 固定 tag，复制 `config.example.yaml`、`.env.example`，创建 `data/license`、`auths`、`logs`、`plugins` 和 `secrets` 目录。
3. 按商城发放结果填写 `CPA_LICENSE_CLIENT_ID`。公开发布默认不要求 `CPA_LICENSE_CLIENT_SECRET`；如需启用服务端 Secret 校验，再将 Secret 放在权限 `600` 的本地文件中，并与商城端同时开启强制校验。
4. CPAMP 只使用本仓库的 `deploy/cpamp-pool-server` 模板和公开 GHCR 镜像，Manager 与 Agent 必须使用同一镜像 tag。
5. 所有 Compose 操作显式使用 `--env-file .env`，启动后检查 CPA `/healthz`、CPAMP `/health` 和面板日志。
6. 升级只替换已验证的 tag/digest，保留 `data/license`、CPA 配置、Manager 数据和备份；不要执行 `down -v`。

也可运行 `install-cpa-cli-release.sh --version v7.2.148-cpa.7-beta.1` 安装预构建 CPA CLI 客户包，或运行 `install-cpamp-release.sh --version v7.2.148-cpa.7-beta.1` 安装 CPAMP 客户包。Beta 使用配套预构建二进制与镜像；源码编译安装器仅用于其对应的稳定源码版本。

商城授权地址为 `https://p.666ttt.net/api/storefront`。正式授权和到期宽限租约由商城签名；本地网络故障兜底最多 6 小时，修改客户机配置不会延长商城租约。公开版如果遗留了旧 Secret，服务端返回 `401 provider_rejected` 时客户端会自动重试一次并移除 `Authorization`，不需要手工改代码。若客户仍使用旧安装包，必须重新下载包含本修复的最新组合 Release；只刷新页面或只修改 `.env` 不会替换已运行的二进制。真实 Secret、授权租约和客户数据不得提交到 Git。

## 参考

- [CLIProxyAPI 用户手册](https://help.router-for.me/cn/)
- [CPA-Manager-Pro 完整 pool-server 模板（仅供维护者参考）](https://github.com/abc124774961/CPA-Manager-Pro/tree/main/deploy/pool-server)
- [GitHub Releases](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases)

其他功能、SDK、管理 API 和上游变更说明请直接参阅上述源码与用户手册，本发布仓库不重复维护。

## 生态引用

### [All API Hub](https://github.com/qixing-jk/all-api-hub)

用于一站式管理 New API 兼容中转站账号的浏览器扩展，提供余额与用量看板、自动签到、密钥一键导出到常用应用、网页内 API 可用性测试，以及渠道与模型同步和重定向。支持通过 CLIProxyAPI Management API 一键导入 Provider 与同步配置。
