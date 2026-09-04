#!/usr/bin/env bash

# Download and install a CPAMP customer bundle from the public release hub.
# The installer never clones or queries the private CPAMP source repository.

set -Eeuo pipefail
# Keep tar/sha tooling deterministic on minimal images whose inherited
# C.UTF-8 locale is not installed.
export LC_ALL=C

release_repo="${CPAMP_RELEASE_REPO:-abc124774961/CLIProxyAPI-CPA-Releases}"
release_version="${CPAMP_RELEASE_VERSION:-v7.2.148-cpa.5}"
install_dir="${CPAMP_INSTALL_DIR:-}"
allow_existing="${CPAMP_ALLOW_EXISTING:-0}"
load_image="${CPAMP_LOAD_IMAGE:-0}"
run_bootstrap="${CPAMP_RUN_BOOTSTRAP:-0}"
dry_run="${CPAMP_DRY_RUN:-0}"
tmp_dir=""

die() {
  printf '错误：%s\n' "$*" >&2
  exit 1
}

info() {
  printf '信息：%s\n' "$*"
}

usage() {
  cat <<'USAGE'
用法：
  install-cpamp-release.sh [选项]

选项：
  --version TAG       公开发布仓库的 CPA 发布 tag（默认 v7.2.148-cpa.5）
  --dir PATH          安装目录（默认 /opt/cpamp 或 $HOME/cpamp）
  --allow-existing    允许写入已有安装目录
  --load-image        同时加载包内的 CPAMP Docker 镜像归档
  --run-bootstrap     下载后直接运行 deploy/cpamp-pool-server/bootstrap.sh
  --dry-run           只显示下载和安装计划，不写入文件
  -h, --help          显示帮助

环境变量：
  CPAMP_RELEASE_REPO、CPAMP_RELEASE_VERSION、CPAMP_INSTALL_DIR、
  CPAMP_ALLOW_EXISTING=1、CPAMP_LOAD_IMAGE=1、CPAMP_RUN_BOOTSTRAP=1、
  CPAMP_DRY_RUN=1、CPAMP_ASSET_BASE_URL、CPAMP_RAW_BASE_URL
USAGE
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --version)
      [[ "$#" -ge 2 ]] || die "--version 需要一个 tag"
      release_version="$2"
      shift 2
      ;;
    --dir)
      [[ "$#" -ge 2 ]] || die "--dir 需要一个路径"
      install_dir="$2"
      shift 2
      ;;
    --allow-existing)
      allow_existing=1
      shift
      ;;
    --load-image)
      load_image=1
      shift
      ;;
    --run-bootstrap)
      run_bootstrap=1
      shift
      ;;
    --dry-run)
      dry_run=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "未知参数：$1"
      ;;
  esac
done

[[ "$release_repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "CPAMP_RELEASE_REPO 格式必须为 OWNER/REPOSITORY"
[[ "$release_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-cpa\.[0-9]+$ ]] || die "发布 tag 格式不正确：$release_version"
[[ "$allow_existing" == 0 || "$allow_existing" == 1 ]] || die "CPAMP_ALLOW_EXISTING 必须为 0 或 1"
[[ "$load_image" == 0 || "$load_image" == 1 ]] || die "CPAMP_LOAD_IMAGE 必须为 0 或 1"
[[ "$run_bootstrap" == 0 || "$run_bootstrap" == 1 ]] || die "CPAMP_RUN_BOOTSTRAP 必须为 0 或 1"
[[ "$dry_run" == 0 || "$dry_run" == 1 ]] || die "CPAMP_DRY_RUN 必须为 0 或 1"

if [[ -z "$install_dir" ]]; then
  if [[ "$(id -u)" == 0 ]]; then
    install_dir="/opt/cpamp"
  else
    install_dir="${HOME:-.}/cpamp"
  fi
fi
if [[ "$install_dir" != /* ]]; then
  install_dir="$PWD/$install_dir"
fi

info "目标版本：${release_version}"
info "公开发布地址：https://github.com/${release_repo}/releases/download/${release_version}"
info "安装目录：${install_dir}"
if [[ "$dry_run" == 1 ]]; then
  info "dry-run：不会下载、解压或启动服务"
  exit 0
fi

case "$(uname -s 2>/dev/null || printf unknown)" in
  Linux) ;;
  *) die "客户部署包仅支持 Linux 主机" ;;
esac
case "$(uname -m 2>/dev/null || printf unknown)" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) die "当前架构不在支持范围内，需要 linux/amd64 或 linux/arm64" ;;
esac

command -v curl >/dev/null 2>&1 || die "需要 curl"
command -v python3 >/dev/null 2>&1 || die "需要 python3"
command -v tar >/dev/null 2>&1 || die "需要 tar"
if [[ "$load_image" == 1 ]]; then
  command -v docker >/dev/null 2>&1 || die "--load-image 需要 Docker"
fi

asset_base="${CPAMP_ASSET_BASE_URL:-https://github.com/${release_repo}/releases/download/${release_version}}"
raw_base="${CPAMP_RAW_BASE_URL:-https://raw.githubusercontent.com/${release_repo}/${release_version}}"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/cpamp-install.XXXXXX")"
cleanup() {
  [[ -n "$tmp_dir" && -d "$tmp_dir" ]] && rm -rf "$tmp_dir"
}
trap cleanup EXIT

info "目标版本：${release_version}，平台：linux/${arch}"
info "公开发布地址：${asset_base}"
info "安装目录：${install_dir}"

if [[ -e "$install_dir" && "$allow_existing" != 1 ]]; then
  die "安装目录已存在：$install_dir（确认备份后可加 --allow-existing）"
fi

mkdir -p "$tmp_dir/download" "$tmp_dir/extract"
curl --fail --silent --show-error --location \
  "${raw_base}/release-manifest.json" \
  --output "$tmp_dir/download/release-manifest.json"
cpamp_version="$(python3 - "$tmp_dir/download/release-manifest.json" <<'PY'
import json
import sys
from pathlib import Path

manifest = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
version = manifest.get("components", {}).get("cpamp", {}).get("version", "")
if not isinstance(version, str) or not version:
    raise SystemExit("公开清单中缺少 CPAMP 版本")
print(version)
PY
)"
[[ "$cpamp_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-cpa\.[0-9]+$ ]] || die "公开清单中的 CPAMP 版本格式不正确"
cpamp_image="$(python3 - "$tmp_dir/download/release-manifest.json" <<'PY'
import json
import sys
from pathlib import Path

manifest = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
image = manifest.get("components", {}).get("cpamp", {}).get("image", "")
if not isinstance(image, str) or not image or any(char.isspace() for char in image) or "@" in image:
    raise SystemExit("公开清单中的 CPAMP 镜像必须是带 tag 的引用")
print(image)
PY
)"
[[ -n "$cpamp_image" ]] || die "公开清单中缺少 CPAMP 镜像"

bundle_name="CPAMP-${cpamp_version}-linux-${arch}.tar.gz"
curl --fail --silent --show-error --location \
  "${asset_base}/checksums.txt" \
  --output "$tmp_dir/download/checksums.txt"
curl --fail --silent --show-error --location \
  "${asset_base}/${bundle_name}" \
  --output "$tmp_dir/download/${bundle_name}"

# Check the archive member names before extraction. This prevents a malformed
# public asset from writing outside the temporary extraction directory and
# keeps customer configuration/state out of the install target.
if tar -tzf "$tmp_dir/download/${bundle_name}" | awk '
  BEGIN { bad = 0 }
  {
    path = $0
    sub(/^[.][\/]/, "", path)
    if (path ~ /(^|\/)\.\.?($|\/)/ || path ~ /(^|\/)\.env($|\/)/ ||
        path ~ /(^|\/)(secrets|data|backups)(\/|$)/) {
      bad = 1
      print path > "/dev/stderr"
    }
  }
  END { exit bad }
'; then
  :
else
  die "CPAMP 发布包包含不允许的路径或运行数据"
fi

checksum_line="$(awk -v wanted="$bundle_name" '$2 == wanted { print; exit }' "$tmp_dir/download/checksums.txt")"
[[ -n "$checksum_line" ]] || die "checksums.txt 中没有 ${bundle_name}"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$tmp_dir/download" && printf '%s\n' "$checksum_line" | sha256sum -c - >/dev/null) \
    || die "CPAMP 发布包校验失败：${bundle_name}"
else
  (cd "$tmp_dir/download" && printf '%s\n' "$checksum_line" | shasum -a 256 -c - >/dev/null) \
    || die "CPAMP 发布包校验失败：${bundle_name}"
fi

tar -xzf "$tmp_dir/download/${bundle_name}" -C "$tmp_dir/extract"
package_dir="$tmp_dir/extract/CPAMP-${cpamp_version}-linux-${arch}"
[[ -d "$package_dir" ]] || die "发布包目录结构不正确"
[[ -x "$package_dir/deploy/cpamp-pool-server/bootstrap.sh" ]] || die "发布包缺少 bootstrap.sh"
[[ -f "$package_dir/deploy/cpamp-pool-server/compose.yml" ]] || die "发布包缺少 compose.yml"
[[ -x "$package_dir/deploy/cpamp-pool-server/preflight.sh" ]] || die "发布包缺少 preflight.sh"
[[ -f "$package_dir/deploy/cpamp-pool-server/.env.example" ]] || die "发布包缺少 .env.example"
[[ -f "$package_dir/deploy/cpamp-pool-server/.gitignore" ]] || die "发布包缺少模板 .gitignore"
[[ -f "$package_dir/deploy/cpamp-pool-server/README.md" ]] || die "发布包缺少 CPAMP 模板说明"
[[ -f "$package_dir/deploy/cpamp-pool-server/config.yaml.template" ]] || die "发布包缺少 CPA 配置模板"
[[ -x "$package_dir/bin/cpa-manager-plus" ]] || die "发布包缺少 cpa-manager-plus"
[[ -x "$package_dir/bin/cpamp-agent" ]] || die "发布包缺少 cpamp-agent"
[[ -s "$package_dir/management.html" ]] || die "发布包缺少 management.html 面板"
[[ -s "$package_dir/LICENSE" ]] || die "发布包缺少 LICENSE"
[[ -f "$package_dir/docs/deployment-cpa-cpamp.zh-CN.md" ]] || die "发布包缺少中文部署说明"
[[ -f "$package_dir/package-manifest.json" ]] || die "发布包缺少 package-manifest.json"

template_cpamp_image="$(awk -F= '$1 == "CPAMP_IMAGE" { print $2; exit }' \
  "$package_dir/deploy/cpamp-pool-server/.env.example")"
[[ "$template_cpamp_image" == "$cpamp_image" ]] || die \
  "发布包 CPAMP_IMAGE 与公开清单不一致：$template_cpamp_image"

image_archive_path="$(python3 - "$package_dir/package-manifest.json" "$cpamp_version" "linux/${arch}" "$cpamp_image" <<'PY'
import json
import re
import sys
from pathlib import Path

path, expected_version, expected_platform, expected_image = sys.argv[1:]
manifest = json.loads(Path(path).read_text(encoding="utf-8"))
if manifest.get("schema_version") != 1 or manifest.get("component") != "cpamp":
    raise SystemExit("package manifest schema/component mismatch")
if manifest.get("version") != expected_version or manifest.get("platform") != expected_platform:
    raise SystemExit("package manifest version/platform mismatch")
if manifest.get("image") != expected_image or manifest.get("image_load_ref") != expected_image:
    raise SystemExit("package manifest image tag does not match release .env.example")
if manifest.get("binaries") != ["bin/cpa-manager-plus", "bin/cpamp-agent"]:
    raise SystemExit("package manifest binaries mismatch")
if manifest.get("deployment_template") != "deploy/cpamp-pool-server":
    raise SystemExit("package manifest deployment template mismatch")
expected_templates = [
    "deploy/cpamp-pool-server/.env.example",
    "deploy/cpamp-pool-server/.gitignore",
    "deploy/cpamp-pool-server/README.md",
    "deploy/cpamp-pool-server/bootstrap.sh",
    "deploy/cpamp-pool-server/compose.yml",
    "deploy/cpamp-pool-server/config.yaml.template",
    "deploy/cpamp-pool-server/preflight.sh",
]
if manifest.get("template_files") != expected_templates:
    raise SystemExit("package manifest deployment template files are incomplete")
if manifest.get("deployment_guide") != "docs/deployment-cpa-cpamp.zh-CN.md":
    raise SystemExit("package manifest deployment guide mismatch")
image_archive = manifest.get("image_archive")
if image_archive is None:
    if manifest.get("image_archive_tag") is not None:
        raise SystemExit("package manifest image archive tag must be null when archive is omitted")
    print("")
else:
    if image_archive != "image/cpamp-image.tar":
        raise SystemExit("package manifest image archive mismatch")
    if manifest.get("image_archive_tag") != expected_image:
        raise SystemExit("package manifest image archive tag does not match release image")
    print(image_archive)
expected_contents = [
    "bin/cpa-manager-plus",
    "bin/cpamp-agent",
    "management.html",
    "deploy/cpamp-pool-server",
    "docs/deployment-cpa-cpamp.zh-CN.md",
]
if image_archive is not None:
    expected_contents.append("image/cpamp-image.tar")
if manifest.get("contents") != expected_contents:
    raise SystemExit("package manifest contents are incomplete or contain an invalid path")
if not re.fullmatch(r"sha256:[0-9a-f]{64}", str(manifest.get("image_digest", ""))):
    raise SystemExit("package manifest image digest is invalid")
if not re.fullmatch(r"sha256:[0-9a-f]{64}", str(manifest.get("platform_digest", ""))):
    raise SystemExit("package manifest platform digest is invalid")
PY
)"
if [[ -n "$image_archive_path" ]]; then
  [[ -f "$package_dir/$image_archive_path" ]] || die "发布包声明了镜像归档但文件不存在：$image_archive_path"
else
  [[ "$load_image" != 1 ]] || die "当前 CPAMP 发布包未包含镜像归档，不能使用 --load-image；请在线拉取 GHCR 镜像"
  info "当前 CPAMP 包未包含镜像归档，将由 bootstrap 在线拉取固定 GHCR 镜像"
fi

mkdir -p "$install_dir"
cp -a "$package_dir/." "$install_dir/"
chmod 0755 "$install_dir/deploy/cpamp-pool-server/bootstrap.sh" \
  "$install_dir/deploy/cpamp-pool-server/preflight.sh"
info "CPAMP ${cpamp_version} 已安装到 ${install_dir}"

if [[ "$load_image" == 1 ]]; then
  image_archive="$install_dir/$image_archive_path"
  [[ -n "$image_archive_path" && -f "$image_archive" ]] || die "发布包缺少镜像归档：$image_archive"
  docker load -i "$image_archive"
  loaded_tags_json="$(docker image inspect --format '{{json .RepoTags}}' "$cpamp_image" 2>/dev/null || true)"
  [[ -n "$loaded_tags_json" && "$loaded_tags_json" != "null" ]] || die \
    "镜像归档已加载，但找不到 .env.example 中的 CPAMP_IMAGE：$cpamp_image"
  python3 - "$loaded_tags_json" "$cpamp_image" <<'PY'
import json
import sys

tags = json.loads(sys.argv[1])
expected = sys.argv[2]
if expected not in (tags or []):
    raise SystemExit(
        f"镜像归档 tag 不匹配：需要 {expected}，实际 tags={tags!r}"
    )
PY
  loaded_arch="$(docker image inspect --format '{{.Architecture}}' "$cpamp_image" 2>/dev/null || true)"
  case "$arch" in
    amd64) expected_loaded_arch="amd64" ;;
    arm64) expected_loaded_arch="arm64" ;;
  esac
  [[ "$loaded_arch" == "$expected_loaded_arch" ]] || die \
    "镜像归档架构不匹配：需要 $expected_loaded_arch，实际 $loaded_arch"
  info "CPAMP 镜像归档已加载并确认 tag=$cpamp_image、架构=linux/$arch；离线启动前请在 deploy/cpamp-pool-server/.env 设置 CPAMP_PULL_POLICY=never"
fi

if [[ "$run_bootstrap" == 1 ]]; then
  info "开始运行 CPAMP bootstrap"
  (cd "$install_dir/deploy/cpamp-pool-server" && ./bootstrap.sh)
else
  info "下一步：cd ${install_dir}/deploy/cpamp-pool-server && ./bootstrap.sh"
fi
