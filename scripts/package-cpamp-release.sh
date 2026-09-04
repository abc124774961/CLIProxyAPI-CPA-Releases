#!/usr/bin/env bash

# Build self-contained CPAMP customer bundles from the published GHCR image.
# The source repository is never required: binaries, the image archive, and
# the checked-in deployment template are assembled from public release inputs.

set -Eeuo pipefail
# Keep tar/sha tooling deterministic on minimal CI images.
export LC_ALL=C
# Prevent macOS tar from adding AppleDouble `._*` members when packaging from a
# local checkout. Linux CI ignores this variable.
export COPYFILE_DISABLE=1

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest_file="${CPAMP_MANIFEST_FILE:-$repo_root/release-manifest.json}"
output_dir="${CPAMP_OUTPUT_DIR:-$repo_root/dist/cpamp}"
include_image="${CPAMP_INCLUDE_IMAGE_ARCHIVE:-1}"
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
[[ "$include_image" == "0" || "$include_image" == "1" ]] || die "CPAMP_INCLUDE_IMAGE_ARCHIVE must be 0 or 1"

# Docker CLI support for `image inspect --platform` and `image save
# --platform` varies across customer hosts and GitHub runner images. A
# platform-specific digest reference already selects the requested image, so
# older CLIs can safely use the plain inspect/save forms as fallbacks.
if docker image inspect --help 2>&1 | grep -q -- '--platform'; then
  docker_inspect_platform_supported=1
else
  docker_inspect_platform_supported=0
fi
docker_image_inspect() {
  local platform="$1"
  shift
  if [[ "$docker_inspect_platform_supported" == 1 ]]; then
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

cpamp_version="$(read_manifest_value components.cpamp.version)"
cpamp_image="$(read_manifest_value components.cpamp.image)"
cpamp_image_digest="$(read_manifest_value components.cpamp.image_digest)"
cpamp_source_commit="$(read_manifest_value components.cpamp.source_commit)"
panel_asset="$(read_manifest_value components.cpamp.panel_asset)"
panel_asset_sha256="$(read_manifest_value components.cpamp.panel_asset_sha256)"

[[ "$cpamp_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-cpa\.[0-9]+$ ]] || die "invalid CPAMP version in release manifest"
[[ "$cpamp_image" != *[[:space:]]* && "$cpamp_image" != *@* ]] || die "CPAMP image must be a tag reference"
[[ "$cpamp_image_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "invalid CPAMP image digest in release manifest"
[[ "$cpamp_source_commit" =~ ^[0-9a-fA-F]{40}$ ]] || die "invalid CPAMP source commit in release manifest"
[[ "$panel_asset" == "management.html" ]] || die "CPAMP panel asset must be management.html"
[[ -s "$repo_root/$panel_asset" ]] || die "CPAMP panel asset is missing: $repo_root/$panel_asset"
[[ "$panel_asset_sha256" =~ ^[0-9a-f]{64}$ ]] || die "invalid CPAMP panel asset checksum in release manifest"
actual_panel_sha256="$(sha256_file "$repo_root/$panel_asset")"
[[ "$actual_panel_sha256" == "$panel_asset_sha256" ]] || die "CPAMP panel asset checksum does not match release manifest"

mkdir -p "$(dirname "$output_dir")"
rm -rf "$output_dir"
mkdir -p "$output_dir"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/cpamp-release.XXXXXX")"

platforms=(linux/amd64 linux/arm64)
artifact_names=()
platform_records=()

template_files=(
  .env.example
  .gitignore
  README.md
  bootstrap.sh
  compose.yml
  config.yaml.template
  preflight.sh
)

read_dotenv_value() {
  local file="$1"
  local key="$2"
  awk -v wanted="$key" '
    /^[[:space:]]*#/ || index($0, "=") == 0 { next }
    {
      name = $0
      sub(/=.*/, "", name)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", name)
      if (name != wanted) next
      value = $0
      sub(/^[^=]*=/, "", value)
      sub(/\r$/, "", value)
      if (length(value) >= 2 && substr(value, 1, 1) == "\"" && substr(value, length(value), 1) == "\"") {
        value = substr(value, 2, length(value) - 2)
      }
      print value
      exit
    }
  ' "$file"
}

verify_archive_tag() {
  local archive_path="$1"
  local expected_tag="$2"
  local expected_arch="$3"
  python3 - "$archive_path" "$expected_tag" "$expected_arch" <<'PY'
import json
import sys
import tarfile

archive_path, expected_tag, expected_arch = sys.argv[1:]
try:
    with tarfile.open(archive_path, "r") as archive:
        member = archive.getmember("manifest.json")
        manifest = json.load(archive.extractfile(member))
except (KeyError, OSError, tarfile.TarError, json.JSONDecodeError) as exc:
    raise SystemExit(f"CPAMP image archive manifest is unreadable: {exc}")

if not isinstance(manifest, list) or len(manifest) != 1:
    raise SystemExit("CPAMP image archive must contain exactly one platform image")
tags = manifest[0].get("RepoTags") or []
if tags != [expected_tag]:
    raise SystemExit(
        "CPAMP image archive tag mismatch: "
        f"expected [{expected_tag!r}], got {tags!r}"
    )
config_name = manifest[0].get("Config")
if not isinstance(config_name, str) or not config_name:
    raise SystemExit("CPAMP image archive manifest has no config descriptor")
try:
    with tarfile.open(archive_path, "r") as archive:
        config = json.load(archive.extractfile(archive.getmember(config_name)))
except (KeyError, OSError, tarfile.TarError, json.JSONDecodeError) as exc:
    raise SystemExit(f"CPAMP image archive config is unreadable: {exc}")
if config.get("os") != "linux" or config.get("architecture") != expected_arch:
    raise SystemExit(
        "CPAMP image archive platform mismatch: "
        f"expected linux/{expected_arch}, got {config.get('os')}/{config.get('architecture')}"
    )
PY
}

for platform in "${platforms[@]}"; do
  arch="${platform#linux/}"
  package_name="CPAMP-${cpamp_version}-${platform//\//-}"
  package_dir="$work_dir/$package_name"
  platform_digest="$(read_manifest_value "components.cpamp.platform_digests.${platform}")"
  # Pull the platform manifest digest, not the multi-arch index digest. Docker
  # cannot address an index digest with `image inspect --platform` reliably on
  # all local stores; the platform digest resolves to the exact image ID that
  # is copied and archived below.
  image_ref="${cpamp_image}@${platform_digest}"
  container_id=""

  [[ "$platform_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "invalid CPAMP platform digest for $platform"

  mkdir -p "$package_dir/bin" "$package_dir/image" "$package_dir/deploy/cpamp-pool-server"
  cp "$repo_root/$panel_asset" "$package_dir/management.html"
  info "pulling ${image_ref} for ${platform}"
  docker pull --platform "$platform" "$image_ref" >/dev/null
  # Pull the canonical tag as well so `docker image save` can preserve the
  # exact tag consumed by Compose on a fresh CI runner.
  docker pull --platform "$platform" "$cpamp_image" >/dev/null
  # A registry platform digest is a manifest digest, whereas Docker's `.Id`
  # may be a config digest (the exact representation differs by image store
  # and engine version). Validate the digest-pinned reference itself instead
  # of relying on that implementation detail.
  repo_digests_json="$(docker_image_inspect "$platform" \
    --format '{{json .RepoDigests}}' "$image_ref" 2>/dev/null || true)"
  # Docker Engine does not consistently populate RepoDigests for an image
  # pulled via a digest-qualified reference (notably on hosted Ubuntu
  # runners). The reference itself is still content-addressed, so an empty
  # RepoDigests value is not a reason to reject an otherwise valid pull. When
  # the engine does expose RepoDigests, verify the expected platform digest;
  # the pinned-reference pull plus the canonical-tag ID comparison below
  # remains the portable integrity check.
  if [[ -n "$repo_digests_json" && "$repo_digests_json" != "null" ]]; then
    python3 - "$repo_digests_json" "$cpamp_image" "$platform_digest" <<'PY'
import json
import sys

digests = json.loads(sys.argv[1])
expected_image, expected_digest = sys.argv[2:]
expected = f"{expected_image}@{expected_digest}"
if expected not in (digests or []) and not any(
    isinstance(value, str) and value.endswith("@" + expected_digest)
    for value in (digests or [])
):
    raise SystemExit(
        f"CPAMP image platform digest mismatch: expected {expected}, got {digests!r}"
    )
PY
  else
    info "Docker did not expose RepoDigests for the pinned CPAMP reference on $platform; continuing with digest-pinned pull and image-ID checks"
  fi
image_arch="$(docker_image_inspect "$platform" --format '{{.Architecture}}' "$image_ref")"
image_os="$(docker_image_inspect "$platform" --format '{{.Os}}' "$image_ref")"
[[ "$image_os" == "linux" && "$image_arch" == "$arch" ]] || die \
  "CPAMP image platform mismatch for $platform: got $image_os/$image_arch"
# The canonical tag is what Compose will resolve after an offline `docker
# load`. Compare it with the digest-pinned reference on the same platform so a
# tag update between the two pulls cannot place an unrelated image in the
# customer archive. The IDs are compared to each other, not to the registry
# manifest digest, because Docker engines represent those values differently.
pinned_image_id="$(docker_image_inspect "$platform" \
  --format '{{.Id}}' "$image_ref")"
[[ -n "$pinned_image_id" ]] || die "could not resolve CPAMP image ID for $platform"
if [[ "$docker_inspect_platform_supported" == 1 ]]; then
  canonical_image_id="$(docker_image_inspect "$platform" \
    --format '{{.Id}}' "$cpamp_image")"
  [[ "$pinned_image_id" == "$canonical_image_id" ]] || die \
    "CPAMP canonical tag does not resolve to the pinned $platform image"
else
  info "Docker CLI cannot inspect a tag with --platform; using the digest-pinned CPAMP image for $platform"
fi

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
  docker cp "$container_id:/usr/local/bin/cpa-manager-plus" "$package_dir/bin/cpa-manager-plus"
  docker cp "$container_id:/usr/local/bin/cpamp-agent" "$package_dir/bin/cpamp-agent"
  cleanup_container
  trap cleanup EXIT
  chmod 0755 "$package_dir/bin/cpa-manager-plus" "$package_dir/bin/cpamp-agent"

  # Copy only the checked-in template files. A developer checkout may contain
  # ignored .env, secrets, data, or backup files from a local test run; none
  # of those may enter a public customer artifact.
  for template_file in "${template_files[@]}"; do
    cp "$repo_root/deploy/cpamp-pool-server/$template_file" \
      "$package_dir/deploy/cpamp-pool-server/$template_file"
  done
  template_cpamp_image="$(read_dotenv_value \
    "$package_dir/deploy/cpamp-pool-server/.env.example" CPAMP_IMAGE)"
  [[ "$template_cpamp_image" == "$cpamp_image" ]] || die \
    "CPAMP template image does not match release manifest: $template_cpamp_image"
  cp "$repo_root/LICENSE" "$package_dir/LICENSE"
  # Include the single customer-facing Chinese deployment guide so the
  # extracted bundle remains self-contained; all other project documentation
  # stays in the referenced source repositories.
  mkdir -p "$package_dir/docs"
  cp "$repo_root/docs/deployment-cpa-cpamp.zh-CN.md" \
    "$package_dir/docs/deployment-cpa-cpamp.zh-CN.md"
  cp "$repo_root/release-manifest.json" "$package_dir/release-manifest.json"
  cp "$repo_root/release-catalog.json" "$package_dir/release-catalog.json"
  cat > "$package_dir/README.zh-CN.md" <<'EOF'
# CPAMP __VERSION__ 客户部署包

本包已包含 CPAMP Manager、Agent、固定镜像归档、`management.html` 面板和客户服务器部署模板。
普通用户无需访问 CPAMP 源码仓库；进入 `deploy/cpamp-pool-server` 目录后按其中的中文说明执行。

- 目标平台：__PLATFORM__
- CPAMP 镜像：`__IMAGE__`
- 镜像 digest：`__DIGEST__`
- 构建来源提交：`__SOURCE_COMMIT__`
- 面板文件：`management.html`

在线部署直接运行 `deploy/cpamp-pool-server/bootstrap.sh`。离线部署先执行：

```bash
docker load -i image/cpamp-image.tar
```

然后在 `deploy/cpamp-pool-server/.env` 中将 `CPAMP_PULL_POLICY` 设为 `never`，再运行 bootstrap。
EOF
  sed -i.bak \
    -e "s|__VERSION__|$cpamp_version|g" \
    -e "s|__PLATFORM__|$platform|g" \
    -e "s|__IMAGE__|$cpamp_image|g" \
    -e "s|__DIGEST__|$cpamp_image_digest|g" \
    -e "s|__SOURCE_COMMIT__|$cpamp_source_commit|g" \
    "$package_dir/README.zh-CN.md"
  rm -f "$package_dir/README.zh-CN.md.bak"
  python3 - "$package_dir/package-manifest.json" "$cpamp_version" "$platform" "$cpamp_image" "$cpamp_image_digest" "$platform_digest" "$cpamp_source_commit" "$include_image" "$panel_asset" "$panel_asset_sha256" <<'PY'
import json
import sys
from pathlib import Path

path, version, platform, image, digest, platform_digest, source_commit, include_image, panel_asset, panel_asset_sha256 = sys.argv[1:]
Path(path).write_text(
    json.dumps(
        {
            "schema_version": 1,
            "component": "cpamp",
            "version": version,
            "platform": platform,
            "image": image,
            "image_load_ref": image,
            "image_digest": digest,
            "platform_digest": platform_digest,
            "source_commit": source_commit,
            "panel_asset": panel_asset,
            "panel_asset_sha256": panel_asset_sha256,
            "binaries": ["bin/cpa-manager-plus", "bin/cpamp-agent"],
            "deployment_template": "deploy/cpamp-pool-server",
            "template_files": [
                "deploy/cpamp-pool-server/.env.example",
                "deploy/cpamp-pool-server/.gitignore",
                "deploy/cpamp-pool-server/README.md",
                "deploy/cpamp-pool-server/bootstrap.sh",
                "deploy/cpamp-pool-server/compose.yml",
                "deploy/cpamp-pool-server/config.yaml.template",
                "deploy/cpamp-pool-server/preflight.sh",
            ],
            "deployment_guide": "docs/deployment-cpa-cpamp.zh-CN.md",
            "image_archive": "image/cpamp-image.tar" if include_image == "1" else None,
            "image_archive_tag": image if include_image == "1" else None,
            "contents": [
                "bin/cpa-manager-plus",
                "bin/cpamp-agent",
                "management.html",
                "deploy/cpamp-pool-server",
                "docs/deployment-cpa-cpamp.zh-CN.md",
            ] + (["image/cpamp-image.tar"] if include_image == "1" else []),
        },
        ensure_ascii=False,
        indent=2,
    )
    + "\n",
    encoding="utf-8",
)
PY

  if [[ "$include_image" == "1" ]]; then
    # Docker Desktop reports the platform manifest digest as the inspection
    # ID, which is not a taggable local image ID. Use the platform-aware save
    # operation so the archive keeps the exact canonical tag consumed by the
    # public Compose template while containing one requested architecture.
    docker_image_save_platform "$platform" "$cpamp_image" "$pinned_image_id" \
      "$package_dir/image/cpamp-image.tar"
    verify_archive_tag "$package_dir/image/cpamp-image.tar" "$cpamp_image" "$arch"
  else
    rm -rf "$package_dir/image"
  fi

  required_package_files=(
    bin/cpa-manager-plus
    bin/cpamp-agent
    management.html
    LICENSE
    README.zh-CN.md
    release-manifest.json
    release-catalog.json
    docs/deployment-cpa-cpamp.zh-CN.md
    package-manifest.json
    deploy/cpamp-pool-server/.env.example
    deploy/cpamp-pool-server/.gitignore
    deploy/cpamp-pool-server/README.md
    deploy/cpamp-pool-server/bootstrap.sh
    deploy/cpamp-pool-server/compose.yml
    deploy/cpamp-pool-server/config.yaml.template
    deploy/cpamp-pool-server/preflight.sh
  )
  if [[ "$include_image" == "1" ]]; then
    required_package_files+=(image/cpamp-image.tar)
  fi
  for package_file in "${required_package_files[@]}"; do
    [[ -s "$package_dir/$package_file" ]] || die \
      "CPAMP package is missing or empty: $platform/$package_file"
  done
  [[ -x "$package_dir/bin/cpa-manager-plus" ]] || die \
    "CPAMP Manager binary is not executable: $platform"
  [[ -x "$package_dir/bin/cpamp-agent" ]] || die \
    "CPAMP Agent binary is not executable: $platform"

  archive="$output_dir/${package_name}.tar.gz"
  tar -C "$work_dir" -czf "$archive" "$package_name"
  artifact_names+=("$(basename "$archive")")
  platform_records+=("$platform|$archive|$platform_digest")
  info "created $(basename "$archive")"
done

python3 - "$output_dir/CPAMP-${cpamp_version}.manifest.json" "$cpamp_version" "$cpamp_image" "$cpamp_image_digest" "$cpamp_source_commit" "$include_image" "$panel_asset" "$panel_asset_sha256" "${platform_records[@]}" <<'PY'
import json
import sys
from pathlib import Path

path, version, image, digest, source_commit, include_image, panel_asset, panel_asset_sha256, *records = sys.argv[1:]
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
            "component": "cpamp",
            "version": version,
            "image": image,
            "image_load_ref": image,
            "image_digest": digest,
            "platform_digests": platform_digests,
            "source_commit": source_commit,
            "panel_asset": panel_asset,
            "panel_asset_sha256": panel_asset_sha256,
            "platforms": sorted(artifacts),
            "artifacts": artifacts,
            "deployment_template": "deploy/cpamp-pool-server",
            "template_files": [
                "deploy/cpamp-pool-server/.env.example",
                "deploy/cpamp-pool-server/.gitignore",
                "deploy/cpamp-pool-server/README.md",
                "deploy/cpamp-pool-server/bootstrap.sh",
                "deploy/cpamp-pool-server/compose.yml",
                "deploy/cpamp-pool-server/config.yaml.template",
                "deploy/cpamp-pool-server/preflight.sh",
            ],
            "image_archive_tag": image if include_image == "1" else None,
            "contents": [
                "bin/cpa-manager-plus",
                "bin/cpamp-agent",
                "management.html",
                "deploy/cpamp-pool-server",
                "docs/deployment-cpa-cpamp.zh-CN.md",
            ] + (["image/cpamp-image.tar"] if include_image == "1" else []),
        },
        ensure_ascii=False,
        indent=2,
    )
    + "\n",
    encoding="utf-8",
)
PY

artifact_names+=("CPAMP-${cpamp_version}.manifest.json")
(
  cd "$output_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${artifact_names[@]}" > checksums.txt
  else
    shasum -a 256 "${artifact_names[@]}" > checksums.txt
  fi
)

info "CPAMP release bundle completed in $output_dir"
