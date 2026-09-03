#!/usr/bin/env bash

# Validate the customer-side CPA license deployment without printing secrets.
# The script is intentionally read-only apart from Docker Compose's temporary
# config rendering and can be run before the first container start.

set -Eeuo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/check-license-deployment.sh [options]

Options:
  --env-file PATH       Compose environment file (default: .env)
  --compose-file PATH   Compose file (default: docker-compose.yml)
  --provider            POST an empty preflight body to the storefront grace
                        endpoint and fail when it returns 404 or is unreachable
                        (requires the host secret file to exist first)
  -h, --help            Show this help
USAGE
}

env_file=".env"
compose_file="docker-compose.yml"
check_provider=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      [[ $# -ge 2 ]] || { echo "--env-file requires a path" >&2; exit 2; }
      env_file="$2"
      shift 2
      ;;
    --compose-file)
      [[ $# -ge 2 ]] || { echo "--compose-file requires a path" >&2; exit 2; }
      compose_file="$2"
      shift 2
      ;;
    --provider)
      check_provider=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
caller_dir="$PWD"
if [[ "$env_file" != /* ]]; then
  env_file="$caller_dir/$env_file"
fi
if [[ "$compose_file" != /* ]]; then
  compose_file="$caller_dir/$compose_file"
fi
if [[ -f "$compose_file" ]]; then
  compose_dir="$(cd "$(dirname "$compose_file")" && pwd)"
else
  compose_dir="$repo_root"
fi
cd "$repo_root"

failures=0
warnings=0

fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
}

warn() {
  echo "WARN: $*" >&2
  warnings=$((warnings + 1))
}

pass() {
  echo "OK: $*"
}

[[ -f "$env_file" ]] || fail "environment file not found: $env_file"
[[ -f "$compose_file" ]] || fail "Compose file not found: $compose_file"

# Read one dotenv value without sourcing the file. This prevents arbitrary
# shell code in a customer-provided .env from executing during the check.
dotenv_value() {
  local key="$1"
  local value=""
  if [[ -f "$env_file" ]]; then
    value="$(awk -v key="$key" '
      /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
      {
        line = $0
        sub(/^[[:space:]]*export[[:space:]]+/, "", line)
        split(line, pair, "=")
        if (pair[1] == key) {
          sub(/^[^=]*=/, "", line)
          print line
          exit
        }
      }
    ' "$env_file")"
  fi
  # Compose gives process environment values precedence over --env-file values.
  if [[ -n "${!key+x}" ]]; then
    value="${!key}"
  fi
  value="${value#\"}"
  value="${value%\"}"
  value="${value#\'}"
  value="${value%\'}"
  printf '%s' "$(printf '%s' "$value" | sed 's/[[:space:]]*$//')"
}

public_key="$(dotenv_value CPA_LICENSE_PUBLIC_KEY)"
if [[ -z "$public_key" ]]; then
  fail "CPA_LICENSE_PUBLIC_KEY is empty; provide the storefront Ed25519 public key"
elif ! command -v python3 >/dev/null 2>&1; then
  fail "python3 is required to validate the Ed25519 public key"
else
  if python3 - "$public_key" <<'PY'
import base64
import binascii
import sys

value = sys.argv[1].strip()
decoded = None
for candidate in (value, value + "=" * ((4 - len(value) % 4) % 4)):
    try:
        raw = base64.b64decode(candidate.encode("ascii"), altchars=b"-_", validate=False)
    except (ValueError, binascii.Error, UnicodeEncodeError):
        continue
    if len(raw) == 32:
        decoded = raw
        break
if decoded is None:
    try:
        decoded = bytes.fromhex(value[2:] if value.lower().startswith("0x") else value)
    except ValueError:
        decoded = None
if decoded is None or len(decoded) != 32:
    sys.exit(1)
PY
  then
    pass "CPA_LICENSE_PUBLIC_KEY decodes to an Ed25519 public key"
  else
    fail "CPA_LICENSE_PUBLIC_KEY is not a 32-byte base64/base64url/hex Ed25519 key"
  fi
fi

management_password="$(dotenv_value MANAGEMENT_PASSWORD)"
config_path="$(dotenv_value CLI_PROXY_CONFIG_PATH)"
[[ -n "$config_path" ]] || config_path="config.yaml"
if [[ "$config_path" != /* ]]; then
  config_path="$repo_root/$config_path"
fi
if [[ -n "$management_password" ]]; then
  pass "MANAGEMENT_PASSWORD is supplied to the container"
elif [[ -f "$config_path" ]] && awk '
  $0 ~ /^remote-management:/ { in_section=1; next }
  in_section && $0 ~ /^[^[:space:]]/ { in_section=0 }
  in_section && $0 ~ /^[[:space:]]+secret-key:/ {
    value = $0
    sub(/^[^:]*:[[:space:]]*/, "", value)
    gsub(/[[:space:]\042]/, "", value)
    if (value != "") found=1
  }
  END { exit(found ? 0 : 1) }
' "$config_path"; then
  pass "management key is configured in config.yaml"
else
  warn "neither MANAGEMENT_PASSWORD nor remote-management.secret-key is set; management/license routes stay unavailable"
fi

if [[ -f "$compose_file" ]] && grep -q "HOME_JWT is required" "$compose_file"; then
  home_jwt="$(dotenv_value HOME_JWT)"
  if [[ -z "$home_jwt" || "$home_jwt" == "your-home-jwt-here" ]]; then
    fail "HOME_JWT is empty or still a placeholder for cluster deployment"
  else
    pass "HOME_JWT is supplied for cluster deployment"
  fi
fi

client_id="$(dotenv_value CPA_LICENSE_CLIENT_ID)"
direct_client_secret="$(dotenv_value CPA_LICENSE_CLIENT_SECRET)"
if [[ -n "$client_id" ]]; then
  pass "CPA_LICENSE_CLIENT_ID is supplied"
else
  warn "CPA_LICENSE_CLIENT_ID is empty; the storefront may require client authentication"
fi

secret_file_path="$(dotenv_value CPA_LICENSE_CLIENT_SECRET_FILE)"
configured_secret_host_path="$(dotenv_value CPA_LICENSE_CLIENT_SECRET_HOST_PATH)"
secret_host_path="$configured_secret_host_path"
if [[ -n "$secret_file_path" && "$secret_file_path" != "/run/secrets/cpa-license-client-secret" ]]; then
  fail "CPA_LICENSE_CLIENT_SECRET_FILE must match the Compose secret mount path /run/secrets/cpa-license-client-secret"
fi
[[ -n "$secret_host_path" ]] || secret_host_path="./secrets/cpa-license-client-secret"
if [[ "$secret_host_path" != /* ]]; then
  secret_host_path="$compose_dir/$secret_host_path"
fi
if [[ ! -f "$secret_host_path" ]]; then
  fail "CPA_LICENSE_CLIENT_SECRET_HOST_PATH does not point to a file: $secret_host_path"
elif [[ ! -s "$secret_host_path" ]]; then
  if [[ -n "$direct_client_secret" ]]; then
    warn "Docker client-secret file is empty; direct CPA_LICENSE_CLIENT_SECRET will be used"
  else
    warn "Docker client-secret file and CPA_LICENSE_CLIENT_SECRET are both empty; activation may be rejected"
  fi
else
  pass "Docker client-secret file exists and is non-empty"
  if [[ -n "$direct_client_secret" ]]; then
    warn "both client-secret sources are populated; the Docker secret file takes precedence"
  fi
fi

if [[ -n "$direct_client_secret" ]]; then
  pass "CPA_LICENSE_CLIENT_SECRET is supplied"
elif [[ -n "$configured_secret_host_path" && -s "$secret_host_path" ]]; then
  pass "client authentication uses CPA_LICENSE_CLIENT_SECRET_HOST_PATH"
elif [[ -s "$secret_host_path" ]]; then
  pass "client authentication uses the default Docker secret file"
else
  warn "no client secret is configured; this is valid only when the storefront client guard is disabled"
fi

rendered_config="$(mktemp "${TMPDIR:-/tmp}/cpa-compose-config.XXXXXX")"
cleanup() {
  rm -f "$rendered_config"
}
trap cleanup EXIT

if [[ -f "$env_file" && -f "$compose_file" ]]; then
  if docker compose --env-file "$env_file" -f "$compose_file" config >"$rendered_config" 2>/dev/null; then
    pass "Docker Compose configuration renders"
    for required in \
      MANAGEMENT_PASSWORD \
	  ANTIGRAVITY_OAUTH_CLIENT_ID \
	  ANTIGRAVITY_OAUTH_CLIENT_SECRET \
      CPA_LICENSE_PROVIDER \
      CPA_LICENSE_PRODUCT_CODE \
      CPA_LICENSE_PUBLIC_KEY \
      CPA_LICENSE_PLUGIN_PUBLIC_KEY \
      CPA_LICENSE_CLIENT_ID \
      CPA_LICENSE_CLIENT_SECRET \
      CPA_LICENSE_CLIENT_SECRET_FILE \
      CPA_LICENSE_API_BASE_URL \
      CPA_LICENSE_STATE_DIR \
      CPA_LICENSE_SHOP_AUTH_URL \
      CPA_LICENSE_SHOP_EXCHANGE_PATH \
      CPA_LICENSE_ACTIVATE_PATH \
      CPA_LICENSE_GRACE_PATH \
      CPA_LICENSE_REFRESH_PATH \
      CPA_LICENSE_VERIFY_PATH \
      CPA_LICENSE_REFRESH_INTERVAL \
      CPA_LICENSE_GRACE_PERIOD \
      CPA_LICENSE_STORAGE_KEY \
      CPA_LICENSE_EXECUTABLE_SHA256 \
      CPA_LICENSE_CLAIM_PATH; do
      if grep -Eq "^[[:space:]]+$required:" "$rendered_config"; then
        pass "Compose passes $required"
      else
        fail "Compose does not pass $required"
      fi
    done
    if grep -q "/CLIProxyAPI/data/license" "$rendered_config"; then
      pass "license state volume is persisted at /CLIProxyAPI/data/license"
    else
      fail "license state volume is missing; container recreation would lose the lease"
    fi
    if grep -Eq "target:[[:space:]]+(/run/secrets/)?cpa-license-client-secret([[:space:]]|$)" "$rendered_config"; then
      pass "client-secret Docker secret uses the expected container path"
    else
      fail "client-secret Docker secret target is missing or mismatched"
    fi
    if grep -Eq "pull_policy: (build|always|missing|never|daily|every_[0-9]+[smhd])" "$rendered_config"; then
      pass "image pull policy is explicit"
    else
      fail "Compose image pull policy is missing or invalid"
    fi
  else
    fail "Docker Compose configuration failed to render (check env values and YAML)"
  fi
fi

if [[ "$check_provider" -eq 1 ]]; then
  base_url="$(dotenv_value CPA_LICENSE_API_BASE_URL)"
  [[ -n "$base_url" ]] || base_url="https://p.666ttt.net/api/storefront"
  grace_path="$(dotenv_value CPA_LICENSE_GRACE_PATH)"
  [[ -n "$grace_path" ]] || grace_path="/licenses/grace"
  [[ "$grace_path" == /* ]] || grace_path="/$grace_path"
  grace_url="${base_url%/}${grace_path}"
  display_url="${grace_url%%\?*}"
  if ! command -v curl >/dev/null 2>&1; then
    fail "curl is required for --provider"
  else
    provider_code="$(curl -sS -o /dev/null -w '%{http_code}' \
      --connect-timeout 5 --max-time 10 \
      -X POST "$grace_url" \
      -H 'Content-Type: application/json' \
      -d '{}' || printf '000')"
    case "$provider_code" in
      404|405|000)
        fail "storefront grace endpoint returned HTTP $provider_code: $display_url"
        ;;
      3??)
        fail "storefront grace endpoint redirected with HTTP $provider_code: $display_url"
        ;;
      5??)
        fail "storefront grace endpoint returned HTTP $provider_code: $display_url"
        ;;
      *)
        pass "storefront grace endpoint is present (HTTP $provider_code): $display_url"
        ;;
    esac
  fi
fi

if [[ "$failures" -gt 0 ]]; then
  echo "License deployment check failed: $failures failure(s), $warnings warning(s)." >&2
  exit 1
fi
echo "License deployment check passed: $warnings warning(s)."
