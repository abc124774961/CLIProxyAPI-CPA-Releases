#!/usr/bin/env bash

# Build self-contained CPA CLI customer bundles from the published GHCR image.
# The bundle contains the tested Linux binary, configuration templates, an
# optional offline image archive, and a small launcher. It never includes a
# customer .env, secret, token, or runtime state directory.

set -Eeuo pipefail
export LC_ALL=C
# Prevent macOS tar from adding AppleDouble `._*` members when the checkout
# carries extended attributes. Linux CI ignores this variable.
export COPYFILE_DISABLE=1

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest_file="${CPA_MANIFEST_FILE:-$repo_root/release-manifest.json}"
output_dir="${CPA_OUTPUT_DIR:-$repo_root/dist/cpa-cli}"
include_image="${CPA_INCLUDE_IMAGE_ARCHIVE:-1}"
work_dir=""

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

info() {
  printf 'INFO: %s\n' "$*"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    LC_ALL=C shasum -a 256 "$1" | awk '{print $1}'
  else
    die "sha256sum or shasum is required"
  fi
}

cleanup() {
  if [[ -n "$work_dir" && -d "$work_dir" ]]; then
    rm -rf "$work_dir"
  fi
}
trap cleanup EXIT

command -v docker >/dev/null 2>&1 || die "docker is required"
command -v python3 >/dev/null 2>&1 || die "python3 is required"
docker info >/dev/null 2>&1 || die "Docker daemon is not available"
[[ -f "$manifest_file" ]] || die "release manifest is missing: $manifest_file"
[[ "$include_image" == "0" || "$include_image" == "1" ]] || die "CPA_INCLUDE_IMAGE_ARCHIVE must be 0 or 1"

# Docker CLI support for `image inspect --platform` and `image save
# --platform` varies across customer hosts and GitHub runner images. A
# platform-specific digest reference already selects the requested image, so
# older CLIs can safely use the plain inspect/save forms as fallbacks.
docker_image_inspect() {
  local platform="$1"
  shift
  if docker image inspect --help 2>&1 | grep -q -- '--platform'; then
    docker image inspect --platform "$platform" "$@"
  else
    docker image inspect "$@"
  fi
}

docker_image_save_platform() {
  local platform="$1"
  local image_ref="$2"
  local image_id="$3"
  local output="$4"
  if docker image save --help 2>&1 | grep -q -- '--platform'; then
    docker image save --platform "$platform" "$image_ref" -o "$output"
  else
    # Older Docker saves all variants reachable from a tag. Retag the exact
    # digest-pinned image ID first so the archive contains one deterministic
    # platform image and retains the canonical Compose tag.
    docker tag "$image_id" "$image_ref"
    docker image save "$image_ref" -o "$output"
  fi
}

read_manifest_value() {
  local expression="$1"
  python3 - "$manifest_file" "$expression" <<'PY'
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

cpa_version="$(read_manifest_value components.cpa_cli.version)"
cpa_image="$(read_manifest_value components.cpa_cli.image)"
cpa_image_digest="$(read_manifest_value components.cpa_cli.image_digest)"
cpa_source_commit="$(read_manifest_value components.cpa_cli.source_commit 2>/dev/null || printf unknown)"

[[ "$cpa_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-cpa\.[0-9]+$ ]] || die "invalid CPA CLI version in release manifest"
[[ "$cpa_image" != *[[:space:]]* && "$cpa_image" != *@* ]] || die "CPA CLI image must be a tag reference"
[[ "$cpa_image_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "invalid CPA CLI image digest in release manifest"

[[ -s "$repo_root/config.example.yaml" ]] || die "config.example.yaml is missing"
[[ -s "$repo_root/.env.example" ]] || die ".env.example is missing"

mkdir -p "$(dirname "$output_dir")"
rm -rf "$output_dir"
mkdir -p "$output_dir"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/cpa-cli-release.XXXXXX")"

platforms=(linux/amd64 linux/arm64)
artifact_names=()
platform_records=()

verify_archive_tag() {
  local archive_path="$1"
  local expected_tag="$2"
  python3 - "$archive_path" "$expected_tag" <<'PY'
import json
import sys
import tarfile

archive_path, expected_tag = sys.argv[1:]
try:
    with tarfile.open(archive_path, "r") as archive:
        manifest = json.load(archive.extractfile(archive.getmember("manifest.json")))
except (KeyError, OSError, tarfile.TarError, json.JSONDecodeError) as exc:
    raise SystemExit(f"CPA image archive manifest is unreadable: {exc}")
if not isinstance(manifest, list) or len(manifest) != 1:
    raise SystemExit("CPA image archive must contain exactly one platform image")
tags = manifest[0].get("RepoTags") or []
if tags != [expected_tag]:
    raise SystemExit(f"CPA image archive tag mismatch: expected [{expected_tag!r}], got {tags!r}")
PY
}

for platform in "${platforms[@]}"; do
  arch="${platform#linux/}"
  package_name="CPA-CLI-${cpa_version}-${platform//\//-}"
  package_dir="$work_dir/$package_name"
  platform_digest="$(read_manifest_value "components.cpa_cli.platform_digests.${platform}")"
  # Resolve and copy the exact platform manifest. The component image digest
  # is the multi-arch index; using it here can make Docker select a different
  # platform or report an index RepoDigest for both bundles.
  image_ref="${cpa_image}@${platform_digest}"
  container_id=""

  [[ "$platform_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "invalid CPA CLI platform digest for $platform"

  mkdir -p "$package_dir/bin" "$package_dir/config" "$package_dir/image" "$package_dir/scripts"
  info "pulling ${image_ref} for ${platform}"
  docker pull --platform "$platform" "$image_ref" >/dev/null
  # Pull the canonical tag as well so `docker image save` can preserve the
  # exact tag consumed by Compose on a fresh CI runner. Verify that the tag
  # resolves to the same registry digest before using it; do not compare a
  # local image/config ID with the registry's platform manifest digest.
  docker pull --platform "$platform" "$cpa_image" >/dev/null
  image_id="$(docker_image_inspect "$platform" --format '{{.Id}}' "$image_ref")"
  [[ "$image_id" =~ ^sha256:[0-9a-f]{64}$ ]] || die "could not resolve CPA image id for $platform"
  image_os="$(docker_image_inspect "$platform" --format '{{.Os}}' "$image_ref")"
  image_arch="$(docker_image_inspect "$platform" --format '{{.Architecture}}' "$image_ref")"
  [[ "$image_os/$image_arch" == "$platform" ]] || die \
    "CPA image platform mismatch for $platform: got $image_os/$image_arch"
  canonical_repo="${cpa_image%:*}"
  expected_platform_repo_digest="${canonical_repo}@${platform_digest}"
  repo_digests_json="$(docker_image_inspect "$platform" --format '{{json .RepoDigests}}' "$image_ref" 2>/dev/null || true)"
  # Some Docker Engine/image-store combinations leave RepoDigests empty for
  # digest-qualified pulls. The pull reference is already content-addressed;
  # when RepoDigests is available, use it as an additional assertion, while
  # the pinned-reference and canonical-tag image-ID checks below remain the
  # portable integrity check.
  if [[ -n "$repo_digests_json" && "$repo_digests_json" != "null" ]]; then
    python3 - "$repo_digests_json" "$expected_platform_repo_digest" "$platform" <<'PY'
import json
import sys

try:
    digests = json.loads(sys.argv[1])
except json.JSONDecodeError as exc:
    raise SystemExit(f"CPA image RepoDigests is not valid JSON: {exc}")
expected_platform, platform = sys.argv[2:]
if expected_platform not in (digests or []):
    raise SystemExit(
        f"CPA image digest mismatch for {platform}: expected {expected_platform!r}, got {digests!r}"
    )
PY
  else
    info "Docker did not expose RepoDigests for the pinned CPA reference on $platform; continuing with digest-pinned pull and image-ID checks"
  fi
  canonical_image_id="$(docker_image_inspect "$platform" --format '{{.Id}}' "$cpa_image")"
  [[ "$canonical_image_id" == "$image_id" ]] || die \
    "CPA canonical tag resolved to a different image for $platform: $canonical_image_id"
  canonical_image_os="$(docker_image_inspect "$platform" --format '{{.Os}}' "$cpa_image")"
  canonical_image_arch="$(docker_image_inspect "$platform" --format '{{.Architecture}}' "$cpa_image")"
  [[ "$canonical_image_os/$canonical_image_arch" == "$platform" ]] || die \
    "CPA canonical tag platform mismatch for $platform: got $canonical_image_os/$canonical_image_arch"

  # The source is a single-platform digest reference, so no create-time
  # --platform flag is needed (and older Docker CLIs do not support it).
  container_id="$(docker create "$image_ref")"
  cleanup_container() {
    if [[ -n "$container_id" ]]; then
      docker rm -f "$container_id" >/dev/null 2>&1 || true
      container_id=""
    fi
  }
  trap 'cleanup_container; cleanup' EXIT
  docker cp "$container_id:/CLIProxyAPI/CLIProxyAPI" "$package_dir/bin/CLIProxyAPI"
  cleanup_container
  trap cleanup EXIT
  chmod 0755 "$package_dir/bin/CLIProxyAPI"

  cp "$repo_root/config.example.yaml" "$package_dir/config/config.example.yaml"
  cp "$repo_root/.env.example" "$package_dir/config/.env.example"
  cp "$repo_root/LICENSE" "$package_dir/LICENSE"
  cp "$repo_root/scripts/check-license-runtime.sh" "$package_dir/scripts/check-license-runtime.sh"
  chmod 0755 "$package_dir/scripts/check-license-runtime.sh"

  cat > "$package_dir/run.sh" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$root"
config_path="${CPA_CONFIG_PATH:-$root/config.yaml}"
if [[ ! -f "$config_path" ]]; then
  cp "$root/config/config.example.yaml" "$config_path"
  chmod 600 "$config_path"
  printf '已创建配置文件：%s\n' "$config_path" >&2
fi
exec "$root/bin/CLIProxyAPI" -config "$config_path" "$@"
EOF
  chmod 0755 "$package_dir/run.sh"

  cat > "$package_dir/README.zh-CN.md" <<'EOF'
# CPA CLI __VERSION__（__PLATFORM__）

本包包含已经验证的 CPA CLI Linux 二进制、配置模板、运行脚本和可选 Docker 镜像归档。
普通用户无需访问源码仓库或在服务器编译。

## 运行

```bash
cp config/.env.example .env
# 按需编辑 .env 和 config/config.example.yaml
./run.sh
```

首次运行会在包目录创建 `config.yaml`。商城授权客户端 ID 和 Secret 只放在本机配置/文件中，
不要提交到 Git。授权状态目录请保持持久化，以便升级后继续使用原授权租约。

## 离线 Docker

```bash
docker load -i image/cli-proxy-api-cpa.tar
```

加载后使用镜像 `__IMAGE__`；归档内的 tag 与 Compose/配置模板一致。

- 镜像 digest：`__DIGEST__`
- 平台 digest：`__PLATFORM_DIGEST__`
- 构建来源提交：`__SOURCE_COMMIT__`
EOF
  sed -i.bak \
    -e "s|__VERSION__|$cpa_version|g" \
    -e "s|__PLATFORM__|$platform|g" \
    -e "s|__IMAGE__|$cpa_image|g" \
    -e "s|__DIGEST__|$cpa_image_digest|g" \
    -e "s|__PLATFORM_DIGEST__|$platform_digest|g" \
    -e "s|__SOURCE_COMMIT__|$cpa_source_commit|g" \
    "$package_dir/README.zh-CN.md"
  rm -f "$package_dir/README.zh-CN.md.bak"

  python3 - "$package_dir/package-manifest.json" "$cpa_version" "$platform" "$cpa_image" "$cpa_image_digest" "$platform_digest" "$cpa_source_commit" "$include_image" <<'PY'
import json
import sys
from pathlib import Path

path, version, platform, image, digest, platform_digest, source_commit, include_image = sys.argv[1:]
Path(path).write_text(
    json.dumps(
        {
            "schema_version": 1,
            "component": "cpa_cli",
            "version": version,
            "platform": platform,
            "image": image,
            "image_load_ref": image,
            "image_digest": digest,
            "platform_digest": platform_digest,
            "source_commit": source_commit,
            "binary": "bin/CLIProxyAPI",
            "config_templates": ["config/config.example.yaml", "config/.env.example"],
            "launcher": "run.sh",
            "license": "LICENSE",
            "runtime_checker": "scripts/check-license-runtime.sh",
            "image_archive": "image/cli-proxy-api-cpa.tar" if include_image == "1" else None,
            "image_archive_tag": image if include_image == "1" else None,
            "contents": [
                "bin/CLIProxyAPI",
                "config/config.example.yaml",
                "config/.env.example",
                "run.sh",
                "scripts/check-license-runtime.sh",
                "LICENSE",
            ] + (["image/cli-proxy-api-cpa.tar"] if include_image == "1" else []),
        },
        ensure_ascii=False,
        indent=2,
    )
    + "\n",
    encoding="utf-8",
)
PY

  if [[ "$include_image" == "1" ]]; then
    # Docker Desktop reports the platform manifest digest as the image
    # inspection ID. That digest is not a taggable local image ID; use the
    # platform-aware image save operation so the archive keeps the canonical
    # Compose tag while containing exactly one requested architecture.
    docker_image_save_platform "$platform" "$cpa_image" "$image_id" \
      "$package_dir/image/cli-proxy-api-cpa.tar"
    verify_archive_tag "$package_dir/image/cli-proxy-api-cpa.tar" "$cpa_image"
  else
    rmdir "$package_dir/image" 2>/dev/null || true
  fi

  required_files=(
    bin/CLIProxyAPI
    config/config.example.yaml
    config/.env.example
    LICENSE
    README.zh-CN.md
    package-manifest.json
    run.sh
    scripts/check-license-runtime.sh
  )
  if [[ "$include_image" == "1" ]]; then
    required_files+=(image/cli-proxy-api-cpa.tar)
  fi
  for required_file in "${required_files[@]}"; do
    [[ -s "$package_dir/$required_file" ]] || die "CPA package is missing or empty: $platform/$required_file"
  done
  [[ -x "$package_dir/bin/CLIProxyAPI" ]] || die "CPA binary is not executable: $platform"

  archive="$output_dir/${package_name}.tar.gz"
  tar -C "$work_dir" -czf "$archive" "$package_name"
  artifact_names+=("$(basename "$archive")")
  platform_records+=("$platform|$archive|$platform_digest")
  info "created $(basename "$archive")"
done

python3 - "$output_dir/CPA-CLI-${cpa_version}.manifest.json" "$cpa_version" "$cpa_image" "$cpa_image_digest" "$cpa_source_commit" "$include_image" "${platform_records[@]}" <<'PY'
import json
import sys
from pathlib import Path

path, version, image, digest, source_commit, include_image, *records = sys.argv[1:]
artifacts = {}
platform_digests = {}
for record in records:
    platform, archive, platform_digest = record.split("|", 2)
    artifacts[platform] = Path(archive).name
    platform_digests[platform] = platform_digest
Path(path).write_text(
    json.dumps(
        {
            "schema_version": 1,
            "component": "cpa_cli",
            "version": version,
            "image": image,
            "image_load_ref": image,
            "image_digest": digest,
            "source_commit": source_commit,
            "platform_digests": platform_digests,
            "platforms": sorted(artifacts),
            "artifacts": artifacts,
            "image_archive_tag": image if include_image == "1" else None,
            "contents": [
                "bin/CLIProxyAPI",
                "config/config.example.yaml",
                "config/.env.example",
                "run.sh",
                "scripts/check-license-runtime.sh",
                "LICENSE",
            ] + (["image/cli-proxy-api-cpa.tar"] if include_image == "1" else []),
        },
        ensure_ascii=False,
        indent=2,
    )
    + "\n",
    encoding="utf-8",
)
PY

artifact_names+=("CPA-CLI-${cpa_version}.manifest.json")
(
  cd "$output_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${artifact_names[@]}" > checksums.txt
  else
    shasum -a 256 "${artifact_names[@]}" > checksums.txt
  fi
)

info "CPA CLI release bundle completed in $output_dir"
