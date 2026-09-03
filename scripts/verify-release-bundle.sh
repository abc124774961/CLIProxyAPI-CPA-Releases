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
  install-cpa-release.sh
  docker-compose.yml
  .env.example
  config.example.yaml
  README_CN.md
  RELEASE-CANDIDATE.md
  scripts/check-license-deployment.sh
  scripts/check-license-deployment_test.sh
  scripts/check-license-runtime.sh
  scripts/check-license-runtime_test.sh
)
for path in "${required_files[@]}"; do
  [[ -f "$path" ]] || { echo "FAIL: release file is missing: $path" >&2; exit 1; }
done
for path in install-cpa-release.sh scripts/check-license-deployment.sh scripts/check-license-runtime.sh scripts/check-license-runtime_test.sh scripts/verify-release-bundle.sh; do
  [[ -x "$path" ]] || { echo "FAIL: release script is not executable: $path" >&2; exit 1; }
done
bash -n install-cpa-release.sh scripts/check-license-deployment.sh scripts/check-license-runtime.sh scripts/check-license-runtime_test.sh scripts/verify-release-bundle.sh

for ignore_entry in '.env' 'data' 'secrets' 'plugins' '*.key'; do
  grep -Fx -- "$ignore_entry" .dockerignore >/dev/null 2>&1 || {
    echo "FAIL: .dockerignore must exclude deployment material: $ignore_entry" >&2
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
