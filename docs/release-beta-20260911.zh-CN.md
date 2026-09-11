# CPA 全套公开 Beta

本次组合版本为 `v7.2.148-cpa.7-beta.2`，CPA CLI 使用同名组件版本，CPAMP Manager 与 Agent 使用 `v1.12.10-cpa.1-beta.1`。两个组件均提供 `linux/amd64`、`linux/arm64` 二进制与镜像归档。

## 发布范围

- CPA CLI、CPAMP Manager、CPAMP Agent、配套 `management.html`。
- 账号独立身份环境、请求与会话并发、会话池、统一出口池及原生 IP 出口、分组与账号出口绑定、按 IP 展示近期请求。
- 固定版本安装器、部署模板、组件及组合清单、统一 SHA-256 校验文件。
- CPAMP 统计成功标记在原始记录与聚合记录中保持一致，包含 SQLite/MySQL 迁移修复。
- CPA 插件同步取消时关闭专用连接，避免读取截止时间被覆盖后继续等待；修正 WebSocket 生命周期测试的预热连接隔离。

本版本标记为 GitHub Pre-release。既有稳定版、默认稳定下载入口和线上池子均保持原状；安装 Beta 需要显式指定版本。组合部署包只包含客户部署文件，不包含本次组件的私有实现源码，安装时使用预构建包或固定镜像。

## 新建 Beta 池

CPA CLI 原生二进制使用 `CGO_ENABLED=1`、Debian 12 构建，宿主机需要 GLIBC 2.36 或更新版本；较老发行版使用随包的 Debian Docker 镜像。CPAMP Manager 与 Agent 使用 `CGO_ENABLED=0`，保留 Alpine 运行镜像，不要求宿主 GLIBC。

```bash
RELEASE_TAG=v7.2.148-cpa.7-beta.2
curl -fL "https://raw.githubusercontent.com/abc124774961/CLIProxyAPI-CPA-Releases/${RELEASE_TAG}/install-cpamp-release.sh" -o install-cpamp-release.sh
bash install-cpamp-release.sh --version "$RELEASE_TAG" --dir /opt/cpa-beta
cd /opt/cpa-beta/deploy/cpamp-pool-server
./bootstrap.sh --render
```

检查生成的 `.env`，按实际环境填写商城授权参数。若与已有服务在同一服务器运行，启动前分别配置 `COMPOSE_PROJECT_NAME`、三个容器名称、网络名称及三个监听端口，使用独立数据目录。然后执行 `./bootstrap.sh`。

仅安装 CPA CLI 时，使用同一 Tag 下的 `install-cpa-cli-release.sh --version "$RELEASE_TAG"`；旧源码编译安装器仅用于其对应的稳定源码版本。

## 可选功能

客户配置示例保持 `routing.high-cache-mode=false`、`codex.cache-affinity.enabled=false`、`shadow=true` 和协议 `legacy` 默认值。代理池、身份环境池及会话池均需显式启用；发布不修改既有账号的请求/会话并发模式。镜像内示例用于描述构建版本支持的字段，客户安装包示例采用上述兼容默认值，实际运行以持久化的客户配置为准。

原生出口 IP 应先绑定到服务器网卡，再加入统一出口池。开启会话池前应选择独立持久化状态路径，同一份可写状态只由一个 CPA 进程持有；同机测试独立副本应使用独立数据、端口与状态路径。

## 固定面板

模板将随包 `management.html` 初始化到 `CPAMP_PANEL_DIR`，以只读目录挂载到 Manager 的 `/panel`，并设置 `PANEL_PATH=/panel/management.html`。CPA 控制面板及其自动更新保持关闭，Beta 面板不从 `releases/latest` 跟随稳定版。

CLI 单独安装默认仅提供 API；配套管理面板通过 CPAMP 的 `management.html` 访问。

已有面板文件会被保留。显式升级已有安装时，先检查账号、会话状态及配置挂载，使用与新 Manager 版本一致的 `management.html` 替换面板；Beta 预检会检查面板版本。目录挂载能观察原子文件替换，避免单文件挂载保留旧 inode。升级服务仅针对当前池子，不操作其他站点。

## 校验与安装边界

下载后使用 Release 中的 `checksums.txt` 校验资产，并检查两个组件 manifest 中的平台和镜像 digest。在线部署从 GHCR 拉取固定版本；离线部署应分别加载 CPA CLI 和 CPAMP 包内镜像，Manager 与 Agent 使用同一 CPAMP 镜像。

离线镜像部署时，在池子的 `.env` 中同时设置 `CPA_PULL_POLICY=never` 和 `CPAMP_PULL_POLICY=never`，并执行 `./bootstrap.sh --no-pull`，避免显式拉取步骤再次访问 GHCR。`install-cpamp-release.sh --load-image` 只加载 CPAMP 镜像，CPA 镜像应从同组合的 CPA CLI 包另行加载；不要直接叠加 `--run-bootstrap` 跳过这一步。

公开发布完成的判据是 Release 可匿名访问、两种架构的两组件资产齐全、下载校验一致，以及 GitHub `releases/latest` 仍指向原稳定版。生成制品或上传 draft 不表示已公开发布。
