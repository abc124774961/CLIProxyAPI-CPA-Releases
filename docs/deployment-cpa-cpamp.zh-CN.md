# CPA CLI + CPAMP 中文部署流程

本页只说明客户服务器的发布版安装、验证和升级。请使用固定版本 tag 或镜像 digest，不使用 `latest`。
普通用户只需本公开仓库中的镜像、Compose 文件、脚本和配置模板，不需要访问或 clone 私有源码仓库。
功能源码和完整参数说明（仅供维护者参考）直接引用：

- [CLIProxyAPI 源码与完整文档](https://github.com/router-for-me/CLIProxyAPI)
- [CPA-Manager-Pro 源码与完整 pool-server 模板](https://github.com/abc124774961/CPA-Manager-Pro/tree/main/deploy/pool-server)

## 当前发布组合

统一公开发布 tag：`v7.2.148-cpa.5`。这是客户获取两项组件、部署模板和安装包的组合锚点；组件版本和镜像 tag 仍按已验证记录保持不变。 清单中的顶层 `version`/`release_tag` 表示组合发布，组件对象中的 `version`/`tag`/镜像 tag 表示实际运行产物。

| 组件 | 组件版本 / 镜像 tag | 镜像 | 组合发布 tag |
| --- | --- | --- | --- |
| CPA CLI | `v7.2.148-cpa.4` | `ghcr.io/abc124774961/cli-proxy-api-cpa:v7.2.148-cpa.4` | `v7.2.148-cpa.5` |
| CPAMP（Manager + Agent） | `v1.12.8-cpa.2` | `ghcr.io/abc124774961/cpa-manager-plus:v1.12.8-cpa.2` | `v7.2.148-cpa.5` |

两项均支持 `linux/amd64` 与 `linux/arm64`。平台 digest、校验和及源码提交见
[release-catalog.json](../release-catalog.json) 和 [release-manifest.json](../release-manifest.json)。

## 1. 准备服务器

安装 Docker Engine、Compose v2、`curl`、`tar` 和 Python 3，确认主机时间、DNS、CA 证书正常，并能通过 HTTPS 访问
`https://p.666ttt.net`。为配置、账号、日志、插件、Manager 数据和授权状态准备持久化目录。
升级时必须保留 `data/license`（或 `CPA_LICENSE_STATE_DIR` 指定目录），不要执行 `down -v`。

## 2. 部署 CPA CLI

```bash
git clone --branch v7.2.148-cpa.5 --depth 1 \
  https://github.com/abc124774961/CLIProxyAPI-CPA-Releases.git /opt/cpa-release
cd /opt/cpa-release
cp config.example.yaml config.yaml
cp .env.example .env
mkdir -p auths logs plugins data/license secrets
chmod 700 data/license
```

编辑 `.env`，至少填写管理密码、商城签发的客户端 ID/Secret，并确认以下地址和状态目录：

```dotenv
CPA_LICENSE_API_BASE_URL=https://p.666ttt.net/api/storefront
CPA_LICENSE_CLIENT_ID=商城签发的客户端ID
# 公开发布默认关闭服务端 Secret 强制校验；启用时必须与商城端配置一致
CPA_LICENSE_REQUIRE_CLIENT_SECRET=false
CPA_LICENSE_CLIENT_SECRET_HOST_PATH=./secrets/cpa-license-client-secret
CLI_PROXY_LICENSE_PATH=./data/license
```

将 Secret 写入 `secrets/cpa-license-client-secret`，权限设为 `600`。不要把真实 `.env`、Secret 或授权状态提交到 Git。

启动前检查并启动 CPA：

```bash
scripts/check-license-deployment.sh --env-file .env --compose-file docker-compose.yml
docker compose --env-file .env -f docker-compose.yml pull
docker compose --env-file .env -f docker-compose.yml up -d cli-proxy-api
curl -fsS http://127.0.0.1:${CLI_PROXY_HOST_PORT:-8317}/healthz
```

需要使用预构建 CPA CLI 包时，可使用仓库中的 [`install-cpa-cli-release.sh`](../install-cpa-cli-release.sh)；该安装器默认使用组合发布 tag `v7.2.148-cpa.5`，按主机架构下载清单登记的 CPA CLI 二进制和镜像归档。若需要从公开仓库 checkout 后本地构建 CPA，再使用 [`install-cpa-release.sh`](../install-cpa-release.sh)。

## 3. 部署 CPAMP

普通用户推荐直接使用公开 Release 安装器。安装器只下载本仓库的 CPAMP 包，包内包含
Manager、Agent、固定镜像归档、Compose 文件和全部部署脚本，不会访问 `CPA-Manager-Pro` 源码仓库：

```bash
RELEASE_TAG=v7.2.148-cpa.5
curl -fL \
  "https://raw.githubusercontent.com/abc124774961/CLIProxyAPI-CPA-Releases/${RELEASE_TAG}/install-cpamp-release.sh" \
  -o install-cpamp-release.sh
chmod +x install-cpamp-release.sh
sudo CPAMP_INSTALL_DIR=/opt/cpa-pool \
  ./install-cpamp-release.sh --version "$RELEASE_TAG"
cd /opt/cpa-pool/deploy/cpamp-pool-server
cp .env.example .env
```

也可以从已经 checkout 的发布仓库复制模板（不要从私有仓库复制）：

```bash
mkdir -p /opt/cpa-pool
cp -a /opt/cpa-release/deploy/cpamp-pool-server/. /opt/cpa-pool/
cd /opt/cpa-pool
cp .env.example .env
```

确认 `CPA_IMAGE` 和 `CPAMP_IMAGE` 与上表完全一致（CPA 镜像 tag 是 `v7.2.148-cpa.4`，CPAMP 镜像 tag 是 `v1.12.8-cpa.2`，不要把组合发布 tag 当成镜像 tag）。Manager 与 Agent 必须使用同一 CPAMP 镜像 tag。
填好商城客户端 ID 和 Secret 后执行：

```bash
chmod +x bootstrap.sh preflight.sh
mkdir -p -m 700 secrets
install -m 600 /path/to/storefront-secret secrets/cpa-license-client-secret
./preflight.sh
docker compose --env-file .env -f compose.yml pull
docker compose --env-file .env -f compose.yml up -d
docker compose --env-file .env -f compose.yml ps
```

如果服务器不能访问 GHCR，可先加载安装包内的 CPAMP 镜像归档，再把 `.env` 中
`CPAMP_PULL_POLICY` 改为 `never`；CPA CLI 镜像仍需在线拉取或由本地镜像仓库提供。

默认面板端口为 `18317`，Agent 端口为 `18417`；同机并行测试时改用未占用端口。面板地址：
`http://<服务器地址>:18317/management.html`。

## 4. 授权验证

确认 CPA `/healthz`、CPAMP `/health` 返回成功，并检查 Manager/Agent 日志。使用运行时脚本检查授权状态：

```bash
scripts/check-license-runtime.sh \
  --base-url http://127.0.0.1:${CLI_PROXY_HOST_PORT:-8317} \
  --config-file ./config.yaml
```

`allowed=true` 且 `reason=active` 表示正式授权；`reason=grace` 或 `reason=expiry_grace` 表示商城签发的宽限窗口。
商城授权和宽限租约由服务端签名，客户端本地网络故障兜底上限为 6 小时；修改客户机配置不会延长租约。公开版若带有历史 Secret，接口返回 `401 provider_rejected` 时会自动再请求一次且不发送 `Authorization`，因此不需要手工删除旧配置。客户必须运行包含该修复的最新组合 Release；旧安装包中的客户端不会因为更新 `.env` 或刷新面板而获得新逻辑。

## 5. 升级与回滚

1. 先备份 `data/license`、CPA 配置、`auths`、Manager 数据和 `secrets/`。
2. 只修改到已验证的 CPA/CPAMP tag 或 digest，先 `pull` 再 `up -d`。
3. 升级后重复健康检查和授权检查。
4. 失败时恢复上一组 tag/digest；不要删除授权目录或执行 `down -v`。

## 参考入口

- [版本目录](../RELEASES_CN.md)
- [CPAMP 模板说明](../deploy/cpamp-pool-server/README.md)
- [GitHub Releases](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases)
- [商城授权入口](https://p.666ttt.net/)
