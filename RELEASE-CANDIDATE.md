# CPA 发布说明

本页仅保留发布入口。发布版本、镜像 digest、平台信息和校验文件以
[release-manifest.json](release-manifest.json)、[release-catalog.json](release-catalog.json) 和
[GitHub Actions workflow](.github/workflows/cpa-release.yml) 为准。

客户部署请阅读 [中文部署流程](docs/deployment-cpa-cpamp.zh-CN.md)，并使用本仓库的
[CPAMP 发布模板](deploy/cpamp-pool-server/README.md)。普通用户不需要访问或 clone 私有的 CPAMP 源码仓库。
功能源码和完整发布变更分别引用（仅供维护者参考）：

- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)
- [CPA-Manager-Pro](https://github.com/abc124774961/CPA-Manager-Pro)

当前组合 Beta tag 为 `v7.2.148-cpa.7-beta.2`；CPA CLI 组件为 `v7.2.148-cpa.7-beta.2`，CPAMP 组件为 `v1.12.10-cpa.1-beta.1`。发布前请运行：

CPA CLI 原生二进制需要 GLIBC 2.36+（CGO、Debian 12 构建），较老系统使用随包 Docker 镜像。CPAMP Manager/Agent 使用 `CGO_ENABLED=0`，保留 Alpine 运行镜像。

```bash
scripts/verify-release-bundle.sh v7.2.148-cpa.7-beta.2
bash scripts/release-beta_test.sh
```

公开仓库不提交客户 `.env`、Secret、授权租约、私钥或客户专用插件包。

Beta 工作流只创建草稿 Pre-release，不移动 latest；维护者在资产下载校验完成后显式公开草稿。稳定版 tag 的既有发布行为保持不变。Beta 组合包使用客户部署文件白名单，不归档组件私有源码。详见 [Beta 安装说明](docs/release-beta-20260911.zh-CN.md)。
