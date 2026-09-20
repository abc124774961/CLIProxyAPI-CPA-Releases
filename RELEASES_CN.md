# CPA 发布版本目录

## 当前正式版

组合版本 `v7.2.148-cpa.7`：CPA CLI `v7.2.148-cpa.7`，CPAMP Manager 与 Agent `v1.12.10-cpa.1`。两组件均提供 `linux/amd64`、`linux/arm64` 预构建二进制和离线镜像包，包含配套 `management.html`、安装器、部署模板和 SHA-256 校验文件。

## 本次变化

- 票据后台维护默认每账号 3 路并发，全局上限 100；各路完成后独立间隔 3 秒继续，不被同账号慢请求阻塞。
- 采集混合使用账号已分配的固定出口与采集代理池，遵守节点容量及明确的上游退避。
- 每账号每模型目标至少 3 张票据，1 小时有效期；配置更新、IP 切换与优雅重启保留仍有效的库存和原到期时间。
- 票据管理、账号后台动作与主可用状态分开展示，提供库存、请求和提示词统计。
- 包含当前版本的请求兼容性、会话、账号导入及票据持久化修复。
- MySQL 升级新增请求分类字段采用即时加列，保留历史数据，避免复制大表阻塞启动。
- 保留商城签名授权和既有的本地网络故障宽限上限。

并发上限不保证上游吞吐或收录成功率。票据长度和接口成功均不代表模型能力结论；现有 Astra low 实测仍出现返回模型与请求模型不一致，尚未确认解决。

## 安装

```bash
RELEASE_TAG=v7.2.148-cpa.7
curl -fL "https://raw.githubusercontent.com/abc124774961/CLIProxyAPI-CPA-Releases/${RELEASE_TAG}/install-cpamp-release.sh" -o install-cpamp-release.sh
bash install-cpamp-release.sh --version "$RELEASE_TAG" --dir /opt/cpa
```

进入包内 `deploy/cpamp-pool-server`，按 [部署指南](docs/deployment-cpa-cpamp.zh-CN.md) 填写配置和商城授权后运行 `bootstrap.sh`。单独安装 Core 使用同版本的 `install-cpa-cli-release.sh`。旧 `install-cpa-release.sh` 继续对应其原有源码版本。

Core 原生二进制基于 Debian 12 构建，需要 GLIBC 2.36+；其他发行版可使用随包 Docker 镜像。Manager 和 Agent 使用静态 Go 构建。升级保留原数据挂载，并同步更新配套面板。

精确源码提交、镜像及平台 digest 见 [release-manifest.json](release-manifest.json)；下载资产以 Release 中的 `checksums.txt` 校验。部署不需要访问私有源码仓库。

## 历史版本

| 组合版本 | 状态 |
| --- | --- |
| [v7.2.148-cpa.7-beta.2](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases/tag/v7.2.148-cpa.7-beta.2) | 公开 Beta |
| [v7.2.148-cpa.6](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases/tag/v7.2.148-cpa.6) | 上一正式版 |

商城授权地址：`https://p.666ttt.net/api/storefront`。客户凭证仅保存在各自部署环境。
