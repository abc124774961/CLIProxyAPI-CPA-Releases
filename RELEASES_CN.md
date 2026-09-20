# CPA 发布版本目录

## 当前正式版

组合版本 `v7.2.148-cpa.8`：CPA CLI `v7.2.148-cpa.8`，CPAMP Manager 与 Agent `v1.12.10-cpa.1`。两组件均提供 `linux/amd64`、`linux/arm64` 预构建二进制和离线镜像包，包含配套 `management.html`、安装器、部署模板和 SHA-256 校验文件。

## 本次变化

本次升级 CPA CLI；Manager、Agent 和配套面板沿用 `v1.12.10-cpa.1` 的已验证产物。

- 新采集票据按同一账号、上游命名空间及最终工作区身份共享，UA、客户端版本等外观差异不再单独阻断复用；历史票据继续按原严格身份规则匹配。
- 按模型、出口和实际 HTTP/WebSocket 协议范围统计库存与补采，每个可用范围目标至少 3 张。保留每账号多并发及业务响应直接收录。
- 撤票只针对实际发送的来源与代次，依据格式合法的 312 字节负反馈，或正常完成响应中的模型不匹配。未知长度、格式错误和错误响应保留为诊断；HTTP 2xx 响应头的合法 312 仍会立即触发撤票。
- 异常退出后保留数据库快照；新增持久化撤销日志，恢复和快照写入均过滤已撤销代次，避免失效票重新出现。日常查询继续走内存。
- 默认有效期统一为 1 小时，普通续采提前 10 分钟；短有效期、慢获取与错峰调度仍受容量保护。容量已满时等待释放，不计为上游失败。
- 已保存票据保留原采集时间、到期时间和历史匹配规则；续采生成新票，不延长旧票期限。

本轮两次 `gpt-6-astra low` 实测均返回 HTTP 200 和预定答案，但上游均报告 `gpt-5.6-luna`。模型一致性仍未解决；票据长度和接口成功不代表模型能力结论。

## 安装

```bash
RELEASE_TAG=v7.2.148-cpa.8
curl -fL "https://raw.githubusercontent.com/abc124774961/CLIProxyAPI-CPA-Releases/${RELEASE_TAG}/install-cpamp-release.sh" -o install-cpamp-release.sh
bash install-cpamp-release.sh --version "$RELEASE_TAG" --dir /opt/cpa
```

进入包内 `deploy/cpamp-pool-server`，按 [部署指南](docs/deployment-cpa-cpamp.zh-CN.md) 填写配置和商城授权后运行 `bootstrap.sh`。单独安装 Core 使用同版本的 `install-cpa-cli-release.sh`。旧 `install-cpa-release.sh` 继续对应其原有源码版本。

Core 原生二进制基于 Debian 12 构建，需要 GLIBC 2.36+；其他发行版可使用随包 Docker 镜像。Manager 和 Agent 使用静态 Go 构建。升级保留原数据挂载，并同步更新配套面板。

从旧版首次升级前，正常停止 Core 后备份运行状态。票据数据库、同目录的 `.revocations` 撤销日志及存在时的 `.recovery-blocked` 文件应作为同一持久化单元备份；回滚涉及数据恢复时使用同一时间点的一致副本，不单独恢复数据库文件。详见 [升级与回滚](docs/deployment-cpa-cpamp.zh-CN.md#5-升级与回滚)。

精确源码提交、镜像及平台 digest 见 [release-manifest.json](release-manifest.json)；下载资产以 Release 中的 `checksums.txt` 校验。部署不需要访问私有源码仓库。

## 历史版本

| 组合版本 | 状态 |
| --- | --- |
| [v7.2.148-cpa.7](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases/tag/v7.2.148-cpa.7) | 上一正式版 |
| [v7.2.148-cpa.7-beta.2](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases/tag/v7.2.148-cpa.7-beta.2) | 公开 Beta |
| [v7.2.148-cpa.6](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases/tag/v7.2.148-cpa.6) | 历史正式版 |

商城授权地址：`https://p.666ttt.net/api/storefront`。客户凭证仅保存在各自部署环境。
