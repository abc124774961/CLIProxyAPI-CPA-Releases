# CPA CLI + CPAMP 统一部署

本页只说明客户服务器的发布版部署入口。源码分别维护在
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 和
[CPA-Manager-Pro](https://github.com/abc124774961/CPA-Manager-Pro)；本仓库登记经过验证的
CPA CLI/CPAMP 版本、镜像、digest 和部署说明。可直接使用本仓库的
[CPAMP pool-server 模板](../deploy/cpamp-pool-server/README.md)，完整参数和上游变更再参考
[独立源码仓库](https://github.com/abc124774961/CPA-Manager-Pro/tree/main/deploy/pool-server)。

## 当前发布组合

| 组件 | 版本/tag | 固定镜像 | 支持架构 |
| --- | --- | --- | --- |
| CPA CLI | `v7.2.148-cpa.3` | `ghcr.io/abc124774961/cli-proxy-api-cpa:v7.2.148-cpa.3` | `linux/amd64`、`linux/arm64` |
| CPAMP（Manager + Agent） | `v1.12.8-cpa.1` / `cpamp-pool-v1.12.8-cpa.1` | `ghcr.io/abc124774961/cpa-manager-plus:v1.12.8-cpa.1` | `linux/amd64`、`linux/arm64` |

镜像 manifest digest、源码提交和历史版本见 [`release-catalog.json`](../release-catalog.json)。生产环境固定
tag；需要更严格的供应链锁定时，使用 catalog 中的 `sha256` digest，不使用 `latest`。

## 快速部署

### 1. 准备 CPA CLI

```bash
git clone --branch v7.2.148-cpa.3 --depth 1 \
  https://github.com/abc124774961/CLIProxyAPI-CPA-Releases.git /opt/cpa-release
cd /opt/cpa-release
cp config.example.yaml config.yaml
cp .env.example .env
mkdir -p auths logs plugins data/license secrets
chmod 700 data/license
```

在 `.env` 中填写管理密码、商城签发的 `CPA_LICENSE_CLIENT_ID`，以及单行的
`CPA_LICENSE_CLIENT_SECRET`（或 `CPA_LICENSE_CLIENT_SECRET_HOST_PATH`）。
`CPA_LICENSE_API_BASE_URL` 使用 `https://p.666ttt.net/api/storefront`；`data/license` 必须在升级时保留。

先执行无密钥配置检查，再启动 CPA：

```bash
scripts/check-license-deployment.sh --env-file .env --compose-file docker-compose.yml
docker compose --env-file .env -f docker-compose.yml pull
docker compose --env-file .env -f docker-compose.yml up -d cli-proxy-api
```

也可以执行 `./install-cpa-release.sh`，脚本会校验固定 tag、磁盘空间、授权目录和运行时状态。

### 2. 准备 CPAMP pool-server

CPAMP 的 Manager 和 Agent 必须使用同一固定版本镜像。使用
[本仓库模板](../deploy/cpamp-pool-server/)，或从当前 release 目录复制到独立目录；再复制 `.env.example`
为 `.env`，并至少设置：

```dotenv
CPA_IMAGE=ghcr.io/abc124774961/cli-proxy-api-cpa:v7.2.148-cpa.3
CPAMP_IMAGE=ghcr.io/abc124774961/cpa-manager-plus:v1.12.8-cpa.1
CPA_LICENSE_PUBLIC_KEY=kJhDRBpfneFdURvPXwiGW3XAmPrd2HVVORfHzP-eYTg
CPA_LICENSE_PLUGIN_PUBLIC_KEY=OHRHVVIlFC34K-5AQUkOPcZLeiSpeX_n_VPbrH3agXQ
CPA_LICENSE_API_BASE_URL=https://p.666ttt.net/api/storefront
CPA_LICENSE_CLIENT_ID=商城签发的客户端ID
CPA_LICENSE_CLIENT_SECRET_HOST_PATH=./secrets/cpa-license-client-secret
```

把商城 Secret 写入 `secrets/cpa-license-client-secret`（权限 `600`），再按模板提供的
`bootstrap.sh`/`preflight.sh` 启动。所有 Compose 操作都显式指定 env 文件：

```bash
./preflight.sh
docker compose --env-file .env -f compose.yml pull
docker compose --env-file .env -f compose.yml up -d
docker compose --env-file .env -f compose.yml ps
```

CPAMP 面板默认端口为 `18317`，Agent 默认端口为 `18417`；与已有实例并行验证时改用未占用端口。

## 验证和升级

```bash
# CPA
curl -fsS http://127.0.0.1:${CLI_PROXY_HOST_PORT:-8317}/healthz

# CPAMP
curl -fsS http://127.0.0.1:${CPAMP_PORT:-18317}/health
```

升级时只替换经过验证的 CPA/CPAMP tag，先 `pull` 再 `up -d`；不要执行 `down -v`，以免删除
`data/license`、CPA 配置、Manager 数据和备份。出现启动或授权异常时，恢复上一组 tag/digest，保留原授权目录后
重新检查。首次激活和到期后的宽限租约由商城签名控制，默认本地兜底上限为 6 小时，修改客户机配置不会把宽限期变成无限期。

## 参考入口

- 版本目录：[`RELEASES_CN.md`](../RELEASES_CN.md) / [`release-catalog.json`](../release-catalog.json)
- CPA CLI 详细安装：[`README_CN.md`](../README_CN.md)
- CPAMP 发布模板：[`deploy/cpamp-pool-server`](../deploy/cpamp-pool-server/)
- CPAMP 源码与详细说明：[CPA-Manager-Pro/deploy/pool-server](https://github.com/abc124774961/CPA-Manager-Pro/tree/main/deploy/pool-server)
- 商城授权地址：[p.666ttt.net](https://p.666ttt.net/)
