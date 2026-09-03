#!/usr/bin/env bash

# Verify that a CPA public-release checkout is complete, internally consistent,
# renderable, and free of customer-specific secrets/state before tagging it.

set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
expected_version="${1:-}"
cd "$repo_root"

for command in git python3 docker; do
  command -v "$command" >/dev/null 2>&1 || { echo "FAIL: $command is required" >&2; exit 1; }
done
docker compose version >/dev/null 2>&1 || { echo "FAIL: Docker Compose v2 is required" >&2; exit 1; }

required_files=(
  release-manifest.json
  release-catalog.json
  install-cpa-release.sh
  docker-compose.yml
  .env.example
  config.example.yaml
  README.md
  README_CN.md
  RELEASES.md
  RELEASES_CN.md
  RELEASE-CANDIDATE.md
  docs/deployment-cpa-cpamp.md
  docs/deployment-cpa-cpamp.zh-CN.md
  deploy/cpamp-pool-server/.env.example
  deploy/cpamp-pool-server/.gitignore
  deploy/cpamp-pool-server/README.md
  deploy/cpamp-pool-server/bootstrap.sh
  deploy/cpamp-pool-server/compose.yml
  deploy/cpamp-pool-server/config.yaml.template
  deploy/cpamp-pool-server/preflight.sh
  scripts/check-license-deployment.sh
  scripts/check-license-deployment_test.sh
  scripts/check-license-runtime.sh
  scripts/check-license-runtime_test.sh
)
for path in "${required_files[@]}"; do
  [[ -f "$path" ]] || { echo "FAIL: release file is missing: $path" >&2; exit 1; }
done
for path in install-cpa-release.sh scripts/check-license-deployment.sh scripts/check-license-runtime.sh scripts/check-license-runtime_test.sh scripts/verify-release-bundle.sh deploy/cpamp-pool-server/bootstrap.sh deploy/cpamp-pool-server/preflight.sh; do
  [[ -x "$path" ]] || { echo "FAIL: release script is not executable: $path" >&2; exit 1; }
done
bash -n install-cpa-release.sh scripts/check-license-deployment.sh scripts/check-license-runtime.sh scripts/check-license-runtime_test.sh scripts/verify-release-bundle.sh deploy/cpamp-pool-server/bootstrap.sh deploy/cpamp-pool-server/preflight.sh

for ignore_entry in '.env' 'data' 'secrets' 'plugins' '*.key'; do
  grep -Fx -- "$ignore_entry" .dockerignore >/dev/null 2>&1 || {
    echo "FAIL: .dockerignore must exclude deployment material: $ignore_entry" >&2
    exit 1
  }
done
for ignore_entry in '.env' 'data/' 'backups/' 'secrets/*'; do
  grep -Fx -- "$ignore_entry" deploy/cpamp-pool-server/.gitignore >/dev/null 2>&1 || {
    echo "FAIL: CPAMP template .gitignore must exclude deployment material: $ignore_entry" >&2
    exit 1
  }
done

# Google OAuth client secrets must be supplied by the deployment environment;
# a public release must never carry a hard-coded GOCSPX credential.
if oauth_secret_hits="$(git grep -nE 'GOCS[P]X-[A-Za-z0-9_-]+' -- ':!scripts/verify-release-bundle.sh')"; then
  echo "FAIL: public release contains a hard-coded Google OAuth client secret:" >&2
  printf '%s\n' "$oauth_secret_hits" >&2
  exit 1
fi

python3 - "$expected_version" <<'PY'
import base64
import hashlib
import json
import re
import sys
from pathlib import Path

expected = sys.argv[1].strip()
manifest = json.loads(Path("release-manifest.json").read_text(encoding="utf-8"))
version = str(manifest.get("version", ""))
if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+-cpa\.[0-9]+", version):
    raise SystemExit("FAIL: release-manifest.json contains an invalid version")
if expected and version != expected:
    raise SystemExit(f"FAIL: manifest version {version} does not match {expected}")
if manifest.get("repository") != "abc124774961/CLIProxyAPI-CPA-Releases":
    raise SystemExit("FAIL: release repository metadata is inconsistent")

license_meta = manifest.get("license") or {}
for field, fingerprint_field in (
    ("public_key", "public_key_sha256"),
    ("plugin_public_key", "plugin_public_key_sha256"),
):
    value = str(license_meta.get(field, ""))
    padded = value + "=" * ((4 - len(value) % 4) % 4)
    try:
        raw = base64.urlsafe_b64decode(padded)
    except Exception as exc:
        raise SystemExit(f"FAIL: {field} is invalid: {exc}")
    if len(raw) != 32:
        raise SystemExit(f"FAIL: {field} must decode to 32 bytes")
    if hashlib.sha256(raw).hexdigest() != license_meta.get(fingerprint_field):
        raise SystemExit(f"FAIL: {field} fingerprint mismatch")

if license_meta.get("api_base_url") != "https://p.666ttt.net/api/storefront":
    raise SystemExit("FAIL: storefront API base URL is inconsistent")
if license_meta.get("grace_authority") != "storefront" or license_meta.get("default_grace_period") != "6h":
    raise SystemExit("FAIL: grace policy metadata is inconsistent")

# The release catalog is the public index for both components. Keep its
# current records aligned with the signed manifest so a deployment cannot
# silently combine a CPA image with a different CPAMP image or tag.
catalog = json.loads(Path("release-catalog.json").read_text(encoding="utf-8"))
if catalog.get("schema_version") != 1:
    raise SystemExit("FAIL: release-catalog.json contains an unsupported schema version")
if catalog.get("repository") != manifest.get("repository"):
    raise SystemExit("FAIL: release catalog repository metadata is inconsistent")
if catalog.get("manifest") != "release-manifest.json":
    raise SystemExit("FAIL: release catalog must point to release-manifest.json")

manifest_components = manifest.get("components") or {}
catalog_components = catalog.get("components") or {}
for component_name in ("cpa_cli", "cpamp"):
    manifest_component = manifest_components.get(component_name) or {}
    catalog_component = catalog_components.get(component_name) or {}
    current = catalog_component.get("current") or {}
    if not manifest_component or not catalog_component or not current:
        raise SystemExit(f"FAIL: release metadata is missing component {component_name}")
    for field in ("version", "tag", "image", "image_digest", "platforms", "platform_digests"):
        if current.get(field) != manifest_component.get(field):
            raise SystemExit(f"FAIL: {component_name} catalog {field} does not match release-manifest.json")
    for field in ("source_repository", "release_url"):
        value = str(manifest_component.get(field, ""))
        if not value.startswith("https://github.com/"):
            raise SystemExit(f"FAIL: {component_name} {field} must be an HTTPS GitHub URL")
    digest = str(current.get("image_digest", ""))
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
        raise SystemExit(f"FAIL: {component_name} image_digest must be a sha256 digest")
    platforms = current.get("platforms")
    if sorted(platforms or []) != ["linux/amd64", "linux/arm64"]:
        raise SystemExit(f"FAIL: {component_name} must publish linux/amd64 and linux/arm64")
    platform_digests = current.get("platform_digests") or {}
    if set(platform_digests) != {"linux/amd64", "linux/arm64"}:
        raise SystemExit(f"FAIL: {component_name} must record both platform digests")
    for platform, platform_digest in platform_digests.items():
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", str(platform_digest)):
            raise SystemExit(f"FAIL: {component_name} {platform} digest is invalid")
    image = str(current.get("image", ""))
    if not image or image.endswith(":latest") or "@sha256:" in image:
        raise SystemExit(f"FAIL: {component_name} image must use an immutable version tag")
    if component_name == "cpa_cli":
        if not str(current.get("checksums_url", "")).endswith("/checksums.txt"):
            raise SystemExit("FAIL: CPA CLI catalog entry must link to checksums.txt")
    else:
        if sorted(current.get("included_binaries") or []) != ["cpa-manager-plus", "cpamp-agent"]:
            raise SystemExit("FAIL: CPAMP catalog entry must include Manager and Agent binaries")
        if not str(current.get("verification_url", "")).startswith("https://github.com/"):
            raise SystemExit("FAIL: CPAMP catalog entry must link to its verification workflow")

    published_tags = catalog_component.get("published_tags")
    if not isinstance(published_tags, list) or not published_tags:
        raise SystemExit(f"FAIL: {component_name} catalog has no published tags")
    for entry in published_tags:
        if not isinstance(entry, dict) or not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+-cpa\.[0-9]+", str(entry.get("version", ""))):
            raise SystemExit(f"FAIL: {component_name} catalog contains an invalid published tag")
        if not str(entry.get("tag_url", "")).startswith("https://github.com/"):
            raise SystemExit(f"FAIL: {component_name} published tag URL is invalid")

deployment = catalog.get("deployment") or {}
for field in ("guide_zh_cn", "guide_en"):
    path = str(deployment.get(field, ""))
    if not path or not Path(path).is_file():
        raise SystemExit(f"FAIL: release catalog deployment guide is missing: {path}")
manifest_deployment = manifest.get("deployment") or {}
for field in ("template_dir", "compose_file", "readme"):
    catalog_path = str(deployment.get(field, ""))
    manifest_path = str(manifest_deployment.get(field, ""))
    if not catalog_path or catalog_path != manifest_path or not Path(catalog_path).exists():
        raise SystemExit(f"FAIL: deployment {field} does not resolve to the checked-in CPAMP template")
for path in (
    "deploy/cpamp-pool-server/.env.example",
    "deploy/cpamp-pool-server/.gitignore",
    "deploy/cpamp-pool-server/README.md",
    "deploy/cpamp-pool-server/bootstrap.sh",
    "deploy/cpamp-pool-server/compose.yml",
    "deploy/cpamp-pool-server/config.yaml.template",
    "deploy/cpamp-pool-server/preflight.sh",
):
    if not Path(path).is_file():
        raise SystemExit(f"FAIL: CPAMP template file is missing: {path}")

def parse_dotenv(path):
    values = {}
    for raw_line in Path(path).read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values.setdefault(key.strip(), value.strip().strip('"\''))
    return values

pool_env_values = parse_dotenv("deploy/cpamp-pool-server/.env.example")
for env_name, component_name in (
    ("CPA_IMAGE", "cpa_cli"),
    ("CPAMP_IMAGE", "cpamp"),
):
    if pool_env_values.get(env_name) != manifest_components[component_name].get("image"):
        raise SystemExit(f"FAIL: CPAMP template {env_name} does not match release-manifest.json")
for env_name, field in (
    ("CPA_LICENSE_PUBLIC_KEY", "public_key"),
    ("CPA_LICENSE_PLUGIN_PUBLIC_KEY", "plugin_public_key"),
):
    if pool_env_values.get(env_name) != license_meta.get(field):
        raise SystemExit(f"FAIL: CPAMP template {env_name} does not match release-manifest.json")
if pool_env_values.get("CPA_LICENSE_CLIENT_SECRET", ""):
    raise SystemExit("FAIL: CPAMP template contains a storefront client secret")
pool_config = Path("deploy/cpamp-pool-server/config.yaml.template").read_text(encoding="utf-8")
panel_repo = re.search(r'(?m)^\s*panel-github-repository:\s*"https://github.com/([^/\"]+/[^\"]+)"', pool_config)
if not panel_repo or panel_repo.group(1) != "abc124774961/CPA-Manager-Pro":
    raise SystemExit("FAIL: CPAMP template panel repository is not the current source repository")
if re.search(r"(?m)^\s*client-secret:\s*[^\s\"']", pool_config):
    raise SystemExit("FAIL: CPAMP config template contains a client secret")
pool_compose = Path("deploy/cpamp-pool-server/compose.yml").read_text(encoding="utf-8")
if re.search(r"(?m)^\s*image:\s*local/", pool_compose):
    raise SystemExit("FAIL: CPAMP compose template uses a local image")

env_values = {}
for raw_line in Path(".env.example").read_text(encoding="utf-8").splitlines():
    line = raw_line.strip()
    if not line or line.startswith("#") or "=" not in line:
        continue
    key, value = line.split("=", 1)
    env_values.setdefault(key.strip(), value.strip().strip('"\''))
for env_name, field in (
    ("CPA_LICENSE_PUBLIC_KEY", "public_key"),
    ("CPA_LICENSE_PLUGIN_PUBLIC_KEY", "plugin_public_key"),
):
    if env_values.get(env_name) != license_meta.get(field):
        raise SystemExit(f"FAIL: {env_name} does not match release-manifest.json")
if env_values.get("CPA_LICENSE_CLIENT_SECRET"):
    raise SystemExit("FAIL: .env.example contains a customer client secret")
compose_text = Path("docker-compose.yml").read_text(encoding="utf-8")
expected_cpa_image = manifest_components["cpa_cli"].get("image")
if f"image: ${{CLI_PROXY_IMAGE:-{expected_cpa_image}}}" not in compose_text:
    raise SystemExit("FAIL: docker-compose.yml default CPA image does not match release-manifest.json")
if "eceasy/cli-proxy-api:latest" in compose_text:
    raise SystemExit("FAIL: docker-compose.yml contains the mutable upstream latest image")

# Parse the small, stable license section in config.example.yaml without
# requiring PyYAML. Public releases must keep both template sources aligned so
# a customer who chooses YAML instead of .env receives the same publisher keys.
config_values = {}
in_license = False
for raw_line in Path("config.example.yaml").read_text(encoding="utf-8").splitlines():
    if re.match(r"^license:\s*(?:#.*)?$", raw_line):
        in_license = True
        continue
    if in_license and raw_line and not raw_line[0].isspace():
        in_license = False
    if not in_license:
        continue
    match = re.match(r"^\s*(public-key|plugin-public-key|client-id|client-secret):\s*(.*?)\s*$", raw_line)
    if not match:
        continue
    value = re.sub(r"\s+#.*$", "", match.group(2)).strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        value = value[1:-1]
    config_values[match.group(1)] = value
for config_name, manifest_name in (
    ("public-key", "public_key"),
    ("plugin-public-key", "plugin_public_key"),
):
    if config_values.get(config_name) != license_meta.get(manifest_name):
        raise SystemExit(f"FAIL: config.example.yaml {config_name} does not match release-manifest.json")
for secret_name in ("client-id", "client-secret"):
    if config_values.get(secret_name, ""):
        raise SystemExit(f"FAIL: config.example.yaml contains a customer {secret_name}")
PY

forbidden="$(git ls-files | awk '
  function ends_with(path, suffix) {
    return length(path) >= length(suffix) && substr(path, length(path) - length(suffix) + 1) == suffix
  }
  {
    count = split($0, parts, "/")
    basename = parts[count]
    if (basename == ".env" || index($0, "/secrets/") || index($0, "/data/license/") || index($0, "/internal/api/data/") || ends_with($0, ".private") || ends_with($0, ".s2plugin")) {
      print
    }
  }
')"
if [[ -n "$forbidden" ]]; then
  echo "FAIL: forbidden customer-specific release paths are tracked:" >&2
  printf '%s\n' "$forbidden" >&2
  exit 1
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/cpa-release-verify.XXXXXX")"
cleanup() { rm -rf "$tmp_dir"; }
trap cleanup EXIT
secret_file="$tmp_dir/client-secret"
env_file="$tmp_dir/release.env"
printf '%s' 'release-verification-secret' > "$secret_file"
chmod 600 "$secret_file"
cp .env.example "$env_file"
cat >> "$env_file" <<EOF
MANAGEMENT_PASSWORD=release-verification-management-key
CPA_LICENSE_CLIENT_ID=release-verification
CPA_LICENSE_CLIENT_SECRET_HOST_PATH=$secret_file
CLI_PROXY_CONFIG_PATH=$repo_root/config.example.yaml
CLI_PROXY_AUTH_PATH=$tmp_dir/auths
CLI_PROXY_LOG_PATH=$tmp_dir/logs
CLI_PROXY_PLUGIN_PATH=$tmp_dir/plugins
CLI_PROXY_LICENSE_PATH=$tmp_dir/license
CLI_PROXY_PULL_POLICY=build
EOF
mkdir -p "$tmp_dir/auths" "$tmp_dir/logs" "$tmp_dir/plugins" "$tmp_dir/license"

docker compose --env-file "$env_file" -f docker-compose.yml config --quiet
bash scripts/check-license-deployment.sh --env-file "$env_file" --compose-file docker-compose.yml
echo "CPA release bundle verification passed (${expected_version:-manifest version})."
