# CPAMP 发布模板

本目录是客户服务器使用的 CPA + CPAMP（Manager + Agent）部署模板，只引用本公开仓库登记的发布镜像和固定版本配置。
普通用户不需要访问或 clone CPA-Manager-Pro 私有源码；完整源码与参数说明链接仅供维护者参考：
[CPA-Manager-Pro](https://github.com/abc124774961/CPA-Manager-Pro/tree/main/deploy/pool-server)。

## 快速安装

本分支是显式选择的 Beta，既有稳定安装保持原状。面板使用本包的固定 HTML 及只读目录挂载，不跟随稳定版的 `releases/latest`。详见 [Beta 安装说明](../../docs/release-beta-20260911.zh-CN.md)。

### 方式一：从公开 Release 安装（推荐）

在客户服务器执行以下命令。安装器只访问公开发布仓库，不需要 GitHub 账号，也不需要
访问 `CPA-Manager-Pro` 源码：

```bash
RELEASE_TAG=v7.2.148-cpa.7-beta.2
curl -fL \
  "https://raw.githubusercontent.com/abc124774961/CLIProxyAPI-CPA-Releases/${RELEASE_TAG}/install-cpamp-release.sh" \
  -o install-cpamp-release.sh
chmod +x install-cpamp-release.sh
sudo CPAMP_INSTALL_DIR=/opt/cpa-pool \
  ./install-cpamp-release.sh --version "$RELEASE_TAG"
cd /opt/cpa-pool/deploy/cpamp-pool-server
cp .env.example .env
```

安装包已包含 Manager、Agent、固定版本镜像归档、Compose 文件和本目录脚本。若使用
`--load-image`，安装器会加载归档中的 CPAMP 镜像；在线部署可直接让 bootstrap 从 GHCR 拉取。

离线镜像部署还应加载配套 CPA CLI 包中的 CPA 镜像，在 `.env` 中设置
`CPA_PULL_POLICY=never`、`CPAMP_PULL_POLICY=never`，并执行 `./bootstrap.sh --no-pull`。

### 方式二：使用本仓库模板

如果已经 checkout 本公开仓库，可将模板复制到独立目录：

```bash
mkdir -p /opt/cpa-pool
cp -a deploy/cpamp-pool-server/. /opt/cpa-pool/
cp management.html /opt/cpa-pool/management.html
cd /opt/cpa-pool
cp .env.example .env
```

然后按下面的步骤填写授权信息并启动。

### 初始化并启动

```bash
chmod +x bootstrap.sh preflight.sh
mkdir -p -m 700 secrets
# 仅在 CPA_LICENSE_REQUIRE_CLIENT_SECRET=true 时需要注入商城 secret
install -m 600 /path/to/storefront-secret secrets/cpa-license-client-secret
./bootstrap.sh
```

公开发布默认不要求 `CPA_LICENSE_CLIENT_SECRET`。只有在 `.env` 中显式设置
`CPA_LICENSE_REQUIRE_CLIENT_SECRET=true`（或 `1`）时，Secret 文件才是启动前置条件；
启用后文件必须是单行、可读且权限为 `600`。脚本不会生成伪 Secret，也不会在输出中显示 Secret。

脚本会生成本地配置和运行目录，执行端口、镜像、公钥、Secret、Compose 插值预检，并使用显式 `--env-file` 拉取固定版本镜像、启动 Manager 与 Agent。已有配置、数据和授权状态默认保留。

面板地址：`http://<服务器地址>:${CPAMP_PORT:-18317}/management.html`。

## 常用命令

```bash
./bootstrap.sh --render       # 只生成配置
./preflight.sh --skip-docker # 只做文件和配置预检
./bootstrap.sh --pull        # 拉取镜像但不启动
./bootstrap.sh --status      # 查看状态
./bootstrap.sh --down        # 停止当前项目
```

Compose 命令必须显式指定环境文件：

```bash
docker compose --env-file .env -f compose.yml <command>
```

## 必要配置

公开组合发布 tag 为 `v7.2.148-cpa.7-beta.2`；`.env` 中至少确认以下组件镜像值与当前发布目录一致（组合 tag 不等于镜像 tag）：清单字段 `release_tag` 只用于定位公开 Release，不要写入 `CPA_IMAGE` 或 `CPAMP_IMAGE`。

```dotenv
CPA_IMAGE=ghcr.io/abc124774961/cli-proxy-api-cpa:v7.2.148-cpa.7-beta.2
CPAMP_IMAGE=ghcr.io/abc124774961/cpa-manager-plus:v1.12.10-cpa.1-beta.1
CPA_LICENSE_API_BASE_URL=https://p.666ttt.net/api/storefront
CPA_LICENSE_CLIENT_ID=商城签发的客户端ID
CPA_LICENSE_REQUIRE_CLIENT_SECRET=false
CPA_LICENSE_CLIENT_SECRET_HOST_PATH=./secrets/cpa-license-client-secret
```

CPA 与 Agent 使用 host network，`CPA_PORT`、`CPAMP_PORT`、`CPAMP_AGENT_PORT` 必须互不相同。容器名、数据目录和端口可在 `.env` 调整，以便同机运行多个隔离实例。

## 授权、宽限和升级

`CPA_LICENSE_PUBLIC_KEY`、`CPA_LICENSE_PLUGIN_PUBLIC_KEY` 为发布公钥，必须保留。授权租约和实例身份写入
`CPA_LICENSE_STATE_DIR`，升级或重建容器时保留该目录。商城签名租约决定正式授权及到期宽限；
`CPA_LICENSE_GRACE_PERIOD=6h` 只是网络故障本地兜底上限，修改本地配置不会变成无限期。
客户端 Secret 默认可选；仅当 `CPA_LICENSE_REQUIRE_CLIENT_SECRET=true` 且商城端同步启用
`STOREFRONT_REQUIRE_LICENSE_CLIENT_SECRET=true` 时才强制校验。

升级前先备份 `data/cpa`、`data/manager`、`secrets/`，只修改到已验证的固定 tag/digest，先执行 `./preflight.sh` 和拉取，再启动。不要执行 `down -v`。脚本默认不覆盖已有 CPA 配置或授权租约。

## 参考

- [统一中文部署流程](../../docs/deployment-cpa-cpamp.zh-CN.md)
- [版本清单](../../release-catalog.json)
- [CPAMP 源码模板](https://github.com/abc124774961/CPA-Manager-Pro/tree/main/deploy/pool-server)
