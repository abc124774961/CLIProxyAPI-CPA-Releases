# CPA 发布说明

本页仅保留发布入口。发布版本、镜像 digest、平台信息和校验文件以
[release-manifest.json](release-manifest.json)、[release-catalog.json](release-catalog.json) 和
[GitHub Actions workflow](.github/workflows/cpa-release.yml) 为准。

客户部署请阅读 [中文部署流程](docs/deployment-cpa-cpamp.zh-CN.md)，并使用本仓库的
[CPAMP 发布模板](deploy/cpamp-pool-server/README.md)。普通用户不需要访问或 clone 私有的 CPAMP 源码仓库。
功能源码和完整发布变更分别引用（仅供维护者参考）：

- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)
- [CPA-Manager-Pro](https://github.com/abc124774961/CPA-Manager-Pro)

当前组合发布 tag 为 `v7.2.148-cpa.6`；CPA CLI 组件仍是 `v7.2.148-cpa.4`，CPAMP 组件仍是 `v1.12.8-cpa.2`。发布前请运行：

```bash
scripts/verify-release-bundle.sh v7.2.148-cpa.6
```

公开仓库不提交客户 `.env`、Secret、授权租约、私钥或客户专用插件包。
