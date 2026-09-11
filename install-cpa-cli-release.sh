#!/usr/bin/env bash

# Install a prebuilt CPA CLI customer bundle from the public release hub.
# The installer downloads only the pinned release assets; it does not clone
# source code or compile on the customer host.

set -Eeuo pipefail
export LC_ALL=C

release_repo="${CPA_CLI_RELEASE_REPO:-${CPA_RELEASE_REPO:-abc124774961/CLIProxyAPI-CPA-Releases}}"
release_tag="${CPA_CLI_RELEASE_TAG:-${CPA_RELEASE_VERSION:-v7.2.148-cpa.6}}"
install_dir="${CPA_CLI_INSTALL_DIR:-${CPA_INSTALL_DIR:-}}"
allow_existing="${CPA_CLI_ALLOW_EXISTING:-0}"
load_image="${CPA_CLI_LOAD_IMAGE:-0}"
run_after_install="${CPA_CLI_RUN:-0}"
dry_run="${CPA_CLI_DRY_RUN:-0}"
asset_base_override="${CPA_CLI_ASSET_BASE_URL:-}"
raw_base_override="${CPA_CLI_RAW_BASE_URL:-}"
tmp_dir=""

fail() {
  printf '错误：%s\n' "$*" >&2
  exit 1
}

info() {
  printf '信息：%s\n' "$*"
}

usage() {
  cat <<'USAGE'
用法：
  install-cpa-cli-release.sh [选项]

选项：
  --version TAG       公开组合发布 tag（默认 v7.2.148-cpa.6）
  --dir PATH          安装目录（默认 /opt/cpa-cli 或 $HOME/cpa-cli）
  --allow-existing    允许更新已有安装目录，保留本地配置和授权状态
  --load-image        加载包内的 CPA Docker 镜像归档
  --run               安装后运行 ./run.sh
  --dry-run           只显示下载计划，不下载、不写入、不运行
  -h, --help          显示帮助

环境变量：
  CPA_CLI_RELEASE_REPO、CPA_CLI_RELEASE_TAG、CPA_CLI_INSTALL_DIR、
  CPA_CLI_ALLOW_EXISTING=1、CPA_CLI_LOAD_IMAGE=1、CPA_CLI_RUN=1、
  CPA_CLI_DRY_RUN=1、CPA_CLI_ASSET_BASE_URL、CPA_CLI_RAW_BASE_URL

兼容旧变量：CPA_RELEASE_REPO、CPA_RELEASE_VERSION、CPA_INSTALL_DIR。
USAGE
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --version)
      [[ "$#" -ge 2 ]] || fail "--version 需要一个 tag"
      release_tag="$2"
      shift 2
      ;;
    --dir)
      [[ "$#" -ge 2 ]] || fail "--dir 需要一个路径"
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
    --run)
      run_after_install=1
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
      fail "未知参数：$1"
      ;;
  esac
done

[[ "$release_repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail "发布仓库格式必须为 OWNER/REPOSITORY"
[[ "$release_tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-cpa\.[0-9]+(-beta\.[1-9][0-9]*)?$ ]] || fail "发布 tag 格式不正确：$release_tag"
for flag_name in allow_existing load_image run_after_install dry_run; do
  flag_value="${!flag_name}"
  [[ "$flag_value" == 0 || "$flag_value" == 1 ]] || fail "$flag_name 必须为 0 或 1"
done

if [[ -z "$install_dir" ]]; then
  if [[ "$(id -u)" == 0 ]]; then
    install_dir="/opt/cpa-cli"
  else
    install_dir="${HOME:-.}/cpa-cli"
  fi
fi
if [[ "$install_dir" != /* ]]; then
  install_dir="$PWD/$install_dir"
fi

asset_base="${asset_base_override:-https://github.com/${release_repo}/releases/download/${release_tag}}"
raw_base="${raw_base_override:-https://raw.githubusercontent.com/${release_repo}/${release_tag}}"
info "组合发布 tag：$release_tag"
info "公开发布地址：$asset_base"
info "安装目录：$install_dir"

if [[ "$dry_run" == 1 ]]; then
  info "dry-run：不会下载、解压、写入或运行"
  exit 0
fi

case "$(uname -s 2>/dev/null || printf unknown)" in
  Linux) ;;
  *) fail "CPA CLI 预编译包仅支持 Linux 主机" ;;
esac
case "$(uname -m 2>/dev/null || printf unknown)" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) fail "当前架构不在支持范围内，需要 linux/amd64 或 linux/arm64" ;;
esac
command -v curl >/dev/null 2>&1 || fail "需要 curl"
command -v python3 >/dev/null 2>&1 || fail "需要 python3"
command -v tar >/dev/null 2>&1 || fail "需要 tar"
if [[ "$load_image" == 1 ]]; then
  command -v docker >/dev/null 2>&1 || fail "--load-image 需要 Docker"
fi

if [[ -e "$install_dir" && "$allow_existing" != 1 ]]; then
  fail "安装目录已存在：$install_dir（确认备份后可加 --allow-existing）"
fi

# Use a temporary directory for download and extraction. The target is only
# changed after every downloaded file and manifest has passed validation.
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/cpa-cli-install.XXXXXX")"
cleanup() {
  [[ -n "$tmp_dir" && -d "$tmp_dir" ]] && rm -rf "$tmp_dir"
}
trap cleanup EXIT
mkdir -p "$tmp_dir/download" "$tmp_dir/extract"

curl --fail --silent --show-error --location \
  "${raw_base}/release-manifest.json" \
  --output "$tmp_dir/download/release-manifest.json"

manifest_value() {
  local expression="$1"
  python3 - "$tmp_dir/download/release-manifest.json" "$expression" <<'PY'
import json
import sys
from pathlib import Path

manifest = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
value = manifest
for part in sys.argv[2].split("."):
    if not isinstance(value, dict) or part not in value:
        raise SystemExit(f"missing manifest field: {sys.argv[2]}")
    value = value[part]
if isinstance(value, (dict, list)):
    raise SystemExit(f"manifest field is not scalar: {sys.argv[2]}")
print(value)
PY
}

manifest_release_tag="$(manifest_value release_tag 2>/dev/null || manifest_value version)"
[[ "$manifest_release_tag" == "$release_tag" ]] || fail \
  "公开清单发布 tag 不匹配：需要 $release_tag，实际 $manifest_release_tag"
component_version="$(manifest_value components.cpa_cli.version)"
component_image="$(manifest_value components.cpa_cli.image)"
component_digest="$(manifest_value components.cpa_cli.image_digest)"
source_commit="$(manifest_value components.cpa_cli.source_commit)"
artifact_manifest_name="$(manifest_value components.cpa_cli.artifact_manifest)"
# Dotted manifest paths cannot address the slash in linux/amd64. Read the
# architecture-specific artifact and digest with a direct JSON helper instead.
read_component_platform() {
  local field="$1"
  local platform="linux/$arch"
  python3 - "$tmp_dir/download/release-manifest.json" "$field" "$platform" <<'PY'
import json
import sys
from pathlib import Path

manifest = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
field, platform = sys.argv[2:]
value = manifest.get("components", {}).get("cpa_cli", {}).get(field, {})
if not isinstance(value, dict) or platform not in value:
    raise SystemExit(f"missing manifest field: components.cpa_cli.{field}.{platform}")
print(value[platform])
PY
}
artifact_name="$(read_component_platform artifacts)"
platform_digest="$(read_component_platform platform_digests)"

[[ "$component_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-cpa\.[0-9]+(-beta\.[1-9][0-9]*)?$ ]] || fail "公开清单中的 CPA CLI 版本格式不正确"
[[ "$component_image" =~ ^[^[:space:]@]+:[^[:space:]@]+$ ]] || fail "公开清单中的 CPA 镜像必须是带 tag 的引用"
[[ "$component_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "公开清单中的 CPA 镜像 digest 不正确"
[[ "$platform_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "公开清单中的平台 digest 不正确"
[[ "$source_commit" =~ ^[0-9a-fA-F]{40}$ ]] || fail "公开清单中的 source_commit 不正确"
[[ "$artifact_manifest_name" =~ ^[A-Za-z0-9_.-]+\.manifest\.json$ ]] || fail "公开清单中的 artifact manifest 文件名不安全"
[[ "$artifact_name" =~ ^[A-Za-z0-9_.-]+\.tar\.gz$ ]] || fail "公开清单中的 CPA 包文件名不安全"

curl --fail --silent --show-error --location \
  "${asset_base}/${artifact_manifest_name}" \
  --output "$tmp_dir/download/${artifact_manifest_name}"
curl --fail --silent --show-error --location \
  "${asset_base}/checksums.txt" \
  --output "$tmp_dir/download/checksums.txt"
curl --fail --silent --show-error --location \
  "${asset_base}/${artifact_name}" \
  --output "$tmp_dir/download/${artifact_name}"

checksum_line="$(awk -v wanted="$artifact_name" '$2 == wanted { print; exit }' "$tmp_dir/download/checksums.txt")"
[[ -n "$checksum_line" ]] || fail "checksums.txt 中没有 $artifact_name"
manifest_checksum_line="$(awk -v wanted="$artifact_manifest_name" '$2 == wanted { print; exit }' "$tmp_dir/download/checksums.txt")"
[[ -n "$manifest_checksum_line" ]] || fail "checksums.txt 中没有 $artifact_manifest_name"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$tmp_dir/download" && printf '%s\n' "$checksum_line" | sha256sum -c - >/dev/null) || \
    fail "CPA CLI 包校验失败：$artifact_name"
  (cd "$tmp_dir/download" && printf '%s\n' "$manifest_checksum_line" | sha256sum -c - >/dev/null) || \
    fail "CPA CLI artifact manifest 校验失败：$artifact_manifest_name"
else
  (cd "$tmp_dir/download" && printf '%s\n' "$checksum_line" | shasum -a 256 -c - >/dev/null) || \
    fail "CPA CLI 包校验失败：$artifact_name"
  (cd "$tmp_dir/download" && printf '%s\n' "$manifest_checksum_line" | shasum -a 256 -c - >/dev/null) || \
    fail "CPA CLI artifact manifest 校验失败：$artifact_manifest_name"
fi

python3 - "$tmp_dir/download/$artifact_manifest_name" "$component_version" "$component_image" "$component_digest" "$source_commit" "linux/$arch" "$artifact_name" "$platform_digest" <<'PY'
import json
import re
import sys
from pathlib import Path

path, expected_version, expected_image, expected_digest, expected_commit, platform, expected_artifact, expected_platform_digest = sys.argv[1:]
data = json.loads(Path(path).read_text(encoding="utf-8"))
if data.get("schema_version") != 1 or data.get("component") != "cpa_cli":
    raise SystemExit("CPA CLI artifact manifest schema/component mismatch")
for key, expected in (
    ("version", expected_version),
    ("image", expected_image),
    ("image_digest", expected_digest),
    ("source_commit", expected_commit),
):
    if data.get(key) != expected:
        raise SystemExit(f"CPA CLI artifact manifest {key} mismatch")
if data.get("platforms") != ["linux/amd64", "linux/arm64"]:
    raise SystemExit("CPA CLI artifact manifest platform list mismatch")
if (data.get("artifacts") or {}).get(platform) != expected_artifact:
    raise SystemExit("CPA CLI artifact manifest artifact name mismatch")
PY

# Inspect archive paths before extraction. Only the public .env.example is
# permitted; customer state, secrets and traversal paths are rejected.
if tar -tzf "$tmp_dir/download/$artifact_name" | awk '
  BEGIN { bad = 0 }
  {
    path = $0
    sub(/^[.][\/]/, "", path)
    if (path ~ /(^|\/)\.\.?($|\/)/ || path ~ /(^|\/)\.env($|\/)/ ||
        path ~ /(^|\/)(secrets|data|backups|auths|logs|plugins)(\/|$)/) {
      if (path !~ /(^|\/)\.env\.example$/) {
        bad = 1
        print path > "/dev/stderr"
      }
    }
  }
  END { exit bad }
'; then
  :
else
  fail "CPA CLI 发布包包含不允许的路径或运行数据"
fi

tar -xzf "$tmp_dir/download/$artifact_name" -C "$tmp_dir/extract"
package_dir="$tmp_dir/extract/CPA-CLI-${component_version}-linux-${arch}"
[[ -d "$package_dir" ]] || fail "CPA CLI 发布包目录结构不正确"

python3 - "$package_dir" "$component_version" "$component_image" "$component_digest" "$source_commit" "linux/$arch" "$platform_digest" <<'PY'
import io
import json
import re
import sys
import tarfile
from pathlib import Path

root, expected_version, expected_image, expected_digest, expected_commit, expected_platform, expected_platform_digest = sys.argv[1:]
root = Path(root)
required = {
    "bin/CLIProxyAPI",
    "config/config.example.yaml",
    "config/.env.example",
    "run.sh",
    "scripts/check-license-runtime.sh",
    "LICENSE",
    "README.zh-CN.md",
    "package-manifest.json",
}
for path in required:
    if not (root / path).is_file() or (root / path).stat().st_size == 0:
        raise SystemExit(f"CPA CLI package file is missing or empty: {path}")
manifest = json.loads((root / "package-manifest.json").read_text(encoding="utf-8"))
if manifest.get("schema_version") != 1 or manifest.get("component") != "cpa_cli":
    raise SystemExit("CPA CLI package manifest schema/component mismatch")
for key, expected in (
    ("version", expected_version),
    ("image", expected_image),
    ("image_load_ref", expected_image),
    ("image_digest", expected_digest),
    ("platform", expected_platform),
    ("platform_digest", expected_platform_digest),
    ("source_commit", expected_commit),
):
    if manifest.get(key) != expected:
        raise SystemExit(f"CPA CLI package manifest {key} mismatch")
if manifest.get("binary") != "bin/CLIProxyAPI":
    raise SystemExit("CPA CLI package binary field mismatch")
if manifest.get("config_templates") != ["config/config.example.yaml", "config/.env.example"]:
    raise SystemExit("CPA CLI package config template list mismatch")
if manifest.get("launcher") != "run.sh" or manifest.get("runtime_checker") != "scripts/check-license-runtime.sh":
    raise SystemExit("CPA CLI package launcher/checker mismatch")
archive_path = manifest.get("image_archive")
if archive_path is None:
    if manifest.get("image_archive_tag") is not None:
        raise SystemExit("CPA CLI package image archive tag must be null when archive is omitted")
else:
    if archive_path != "image/cli-proxy-api-cpa.tar" or not (root / archive_path).is_file():
        raise SystemExit("CPA CLI package image archive path mismatch")
    if manifest.get("image_archive_tag") != expected_image:
        raise SystemExit("CPA CLI package image archive tag mismatch")
    try:
        with tarfile.open(root / archive_path, "r:") as image_archive:
            image_manifest = json.load(image_archive.extractfile(image_archive.getmember("manifest.json")))
    except (KeyError, OSError, tarfile.TarError, json.JSONDecodeError) as exc:
        raise SystemExit(f"CPA CLI Docker image archive is unreadable: {exc}")
    if not isinstance(image_manifest, list) or len(image_manifest) != 1:
        raise SystemExit("CPA CLI Docker image archive must contain one platform image")
    if image_manifest[0].get("RepoTags") != [expected_image]:
        raise SystemExit("CPA CLI Docker image archive tag mismatch")
contents = manifest.get("contents") or []
expected_contents = [
    "bin/CLIProxyAPI",
    "config/config.example.yaml",
    "config/.env.example",
    "run.sh",
    "scripts/check-license-runtime.sh",
    "LICENSE",
]
if archive_path is not None:
    expected_contents.append("image/cli-proxy-api-cpa.tar")
if contents != expected_contents:
    raise SystemExit("CPA CLI package contents are incomplete or contain an invalid path")
# Validate the ELF machine/class without requiring the optional `file` command.
binary = (root / "bin/CLIProxyAPI").read_bytes()
if len(binary) < 20 or binary[:4] != b"\x7fELF" or binary[4] != 2 or binary[5] != 1:
    raise SystemExit("CPA CLI binary is not a 64-bit little-endian Linux ELF")
machine = int.from_bytes(binary[18:20], "little")
expected_machine = {"linux/amd64": 62, "linux/arm64": 183}[expected_platform]
if machine != expected_machine:
    raise SystemExit(f"CPA CLI binary architecture mismatch: machine={machine}, expected={expected_machine}")
PY

mkdir -p "$install_dir"
cp -a "$package_dir/." "$install_dir/"
chmod 0755 "$install_dir/bin/CLIProxyAPI" "$install_dir/run.sh" "$install_dir/scripts/check-license-runtime.sh"
mkdir -p "$install_dir/auths" "$install_dir/logs" "$install_dir/plugins" "$install_dir/data/license" "$install_dir/secrets"
chmod 700 "$install_dir/data/license" "$install_dir/secrets"
if [[ ! -e "$install_dir/config.yaml" ]]; then
  cp "$install_dir/config/config.example.yaml" "$install_dir/config.yaml"
  chmod 600 "$install_dir/config.yaml"
  info "已创建 $install_dir/config.yaml"
fi
[[ -f "$install_dir/config.yaml" ]] || fail "$install_dir/config.yaml 不是普通文件"
if [[ ! -e "$install_dir/.env" ]]; then
  cp "$install_dir/config/.env.example" "$install_dir/.env"
  chmod 600 "$install_dir/.env"
  # The shared template uses the container path for Compose deployments. A
  # direct prebuilt-binary install must keep the signed lease on the host.
  python3 - "$install_dir/.env" "$install_dir/data/license" <<'PY'
from pathlib import Path
import sys

path, state_dir = sys.argv[1:]
lines = Path(path).read_text(encoding="utf-8").splitlines()
updated = False
for index, line in enumerate(lines):
    if line.startswith("CPA_LICENSE_STATE_DIR="):
        lines[index] = f"CPA_LICENSE_STATE_DIR={state_dir}"
        updated = True
        break
if not updated:
    lines.append(f"CPA_LICENSE_STATE_DIR={state_dir}")
Path(path).write_text("\n".join(lines) + "\n", encoding="utf-8")
PY
  info "已创建 $install_dir/.env（授权状态目录已设为 $install_dir/data/license）"
fi
[[ -f "$install_dir/.env" ]] || fail "$install_dir/.env 不是普通文件"

if [[ "$load_image" == 1 ]]; then
  image_archive="$install_dir/image/cli-proxy-api-cpa.tar"
  [[ -f "$image_archive" ]] || fail "发布包未包含镜像归档：$image_archive"
  docker load -i "$image_archive" >/dev/null
  loaded_tags="$(docker image inspect --format '{{json .RepoTags}}' "$component_image" 2>/dev/null || true)"
  [[ -n "$loaded_tags" && "$loaded_tags" != null ]] || fail "镜像归档已加载，但找不到 $component_image"
  python3 - "$loaded_tags" "$component_image" <<'PY'
import json
import sys

tags = json.loads(sys.argv[1])
expected = sys.argv[2]
if expected not in (tags or []):
    raise SystemExit(f"加载后的镜像 tag 不匹配：需要 {expected}，实际 {tags!r}")
PY
  info "CPA CLI 镜像归档已加载：$component_image"
fi

info "CPA CLI ${component_version} 已安装到 $install_dir（组合发布 $release_tag）"
info "请编辑 $install_dir/.env 和 $install_dir/config.yaml，填入商城客户端信息与下游 API key"
info "授权状态目录：$install_dir/data/license（升级时保留）"
if [[ "$run_after_install" == 1 ]]; then
  info "开始运行 CPA CLI"
  exec "$install_dir/run.sh"
else
  info "下一步：cd $install_dir && ./run.sh"
fi
