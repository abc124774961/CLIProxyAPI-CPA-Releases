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
  --provider            Probe the storefront authorization endpoint. The probe
                        accepts only an HTTP 2xx response; 3xx/4xx/5xx and
                        network failures are deployment failures.
  --provider-url URL    Override the probe URL (or set
                        CPA_LICENSE_PREFLIGHT_URL). Use the storefront's
                        non-mutating dry-run endpoint when available.
  --provider-body JSON  Send a complete JSON request body (or set
                        CPA_LICENSE_PREFLIGHT_BODY).
  --provider-body-file PATH
                        Read the JSON request body from a mode-restricted file
                        (or set CPA_LICENSE_PREFLIGHT_BODY_FILE).
  -h, --help            Show this help
USAGE
}

env_file=".env"
compose_file="docker-compose.yml"
check_provider=0
provider_url_override=""
provider_body_override=""
provider_body_file_override=""

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
    --provider-url|--provider-dry-run-url)
      [[ $# -ge 2 ]] || { echo "$1 requires a URL" >&2; exit 2; }
      provider_url_override="$2"
      check_provider=1
      shift 2
      ;;
    --provider-body|--provider-request-body)
      [[ $# -ge 2 ]] || { echo "$1 requires JSON" >&2; exit 2; }
      provider_body_override="$2"
      check_provider=1
      shift 2
      ;;
    --provider-body-file|--provider-request-body-file)
      [[ $# -ge 2 ]] || { echo "$1 requires a path" >&2; exit 2; }
      provider_body_file_override="$2"
      check_provider=1
      shift 2
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

# Read a simple scalar from the customer config without evaluating YAML or
# executing any file content. Existing deployments may keep the public key in
# config.yaml while their environment file contains only runtime overrides.
yaml_license_value() {
  local path="$1"
  local key="$2"
  [[ -f "$path" && -r "$path" ]] || return 0
  awk -v wanted="$key" '
    function trim(s) {
      sub(/^[[:space:]]+/, "", s)
      sub(/[[:space:]]+$/, "", s)
      return s
    }
    /^[[:space:]]*license:[[:space:]]*(#.*)?$/ { in_section=1; next }
    in_section && /^[^[:space:]]/ { in_section=0 }
    in_section && /^[[:space:]]+/ {
      line=$0
      sub(/^[[:space:]]+/, "", line)
      name=line
      sub(/:.*/, "", name)
      if (trim(name) != wanted) next
      value=line
      sub(/^[^:]*:[[:space:]]*/, "", value)
      sub(/[[:space:]]+#.*$/, "", value)
      value=trim(value)
      double_quote=sprintf("%c", 34)
      single_quote=sprintf("%c", 39)
      first=substr(value, 1, 1)
      last=substr(value, length(value), 1)
      if (length(value) >= 2 && ((first == double_quote && last == double_quote) || (first == single_quote && last == single_quote))) {
        value=substr(value, 2, length(value)-2)
      }
      print value
      exit
    }
  ' "$path"
}

# Resolve a config path exactly as Docker Compose resolves a relative bind
# mount: relative paths are rooted at the Compose file directory, while `~`
# paths are rooted at the invoking user's home directory. Keeping this in one
# helper prevents public-key and management-key checks from reading different
# files when a customer uses a custom Compose location.
resolve_config_path() {
  local path="$1"
  case "$path" in
    "~")
      if [[ -n "${HOME:-}" ]]; then
        printf '%s' "$HOME"
      fi
      ;;
    "~/"*)
      if [[ -n "${HOME:-}" ]]; then
        printf '%s/%s' "$HOME" "${path#~/}"
      fi
      ;;
    /*)
      printf '%s' "$path"
      ;;
    *)
      printf '%s/%s' "$compose_dir" "$path"
      ;;
  esac
  return 0
}

validate_ed25519_public_key() {
  local value="$1"
  python3 - "$value" <<'PY'
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
    raise SystemExit(1)
PY
}

config_path="$(dotenv_value CLI_PROXY_CONFIG_PATH)"
[[ -n "$config_path" ]] || config_path="config.yaml"
config_path="$(resolve_config_path "$config_path")"
public_key="$(dotenv_value CPA_LICENSE_PUBLIC_KEY)"
public_key_source="environment"
if [[ -z "$public_key" ]]; then
  public_key="$(yaml_license_value "$config_path" public-key)"
  public_key_source="config.yaml"
fi
plugin_public_key="$(dotenv_value CPA_LICENSE_PLUGIN_PUBLIC_KEY)"
plugin_public_key_source="environment"
if [[ -z "$plugin_public_key" ]]; then
  plugin_public_key="$(yaml_license_value "$config_path" plugin-public-key)"
  plugin_public_key_source="config.yaml"
fi
if [[ -z "$plugin_public_key" && -n "$public_key" ]]; then
  # Legacy configurations used one publisher key for both lease and plugin
  # signatures. Match the runtime fallback so old customer deployments remain
  # valid while a dedicated plugin key can still be checked when present.
  plugin_public_key="$public_key"
  plugin_public_key_source="same-as-public-key"
fi
if [[ -z "$public_key" ]]; then
  fail "CPA_LICENSE_PUBLIC_KEY is empty and license.public-key is missing from $config_path; provide the storefront Ed25519 public key"
elif ! command -v python3 >/dev/null 2>&1; then
  fail "python3 is required to validate the Ed25519 public key"
else
  python_available=1
  if validate_ed25519_public_key "$public_key"
  then
    pass "CPA_LICENSE_PUBLIC_KEY decodes to an Ed25519 public key (source: $public_key_source)"
  else
    fail "CPA_LICENSE_PUBLIC_KEY is not a 32-byte base64/base64url/hex Ed25519 key"
  fi
fi
if [[ -n "$plugin_public_key" ]]; then
  if [[ "${python_available:-0}" != "1" ]]; then
    if command -v python3 >/dev/null 2>&1; then
      python_available=1
    else
      fail "python3 is required to validate the plugin Ed25519 public key"
    fi
  fi
  if [[ "${python_available:-0}" == "1" ]]; then
    if validate_ed25519_public_key "$plugin_public_key"; then
      pass "CPA_LICENSE_PLUGIN_PUBLIC_KEY decodes to an Ed25519 public key (source: $plugin_public_key_source)"
    else
      fail "CPA_LICENSE_PLUGIN_PUBLIC_KEY is not a 32-byte base64/base64url/hex Ed25519 key"
    fi
  fi
fi

host_port="$(dotenv_value CLI_PROXY_HOST_PORT)"
[[ -n "$host_port" ]] || host_port="8317"
if [[ ! "$host_port" =~ ^[0-9]+$ ]] || (( host_port < 1 || host_port > 65535 )); then
  fail "CLI_PROXY_HOST_PORT must be an integer between 1 and 65535"
else
  pass "CPA host port is valid ($host_port -> container 8317)"
fi

container_name="$(dotenv_value CLI_PROXY_CONTAINER_NAME)"
if [[ -z "$container_name" ]]; then
  # The cluster Compose file intentionally uses a distinct default name so it
  # can run beside the standalone service. Mirror that default when the
  # customer leaves CLI_PROXY_CONTAINER_NAME unset.
  case "$(basename "$compose_file")" in
    docker-compose.cluster.yml) container_name="cli-proxy-api-cluster" ;;
    *) container_name="cli-proxy-api" ;;
  esac
fi
if [[ ! "$container_name" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]]; then
  fail "CLI_PROXY_CONTAINER_NAME contains invalid Docker name characters"
else
  pass "CPA container name is valid ($container_name)"
fi

declare -a mapped_host_ports=()
mapped_host_ports+=("$host_port")
for callback_spec in \
  "CLI_PROXY_HOST_PORT_8085:8085" \
  "CLI_PROXY_HOST_PORT_1455:1455" \
  "CLI_PROXY_HOST_PORT_54545:54545" \
  "CLI_PROXY_HOST_PORT_51121:51121" \
  "CLI_PROXY_HOST_PORT_11451:11451"; do
  callback_var="${callback_spec%%:*}"
  callback_container_port="${callback_spec##*:}"
  callback_host_port="$(dotenv_value "$callback_var")"
  [[ -n "$callback_host_port" ]] || callback_host_port="$callback_container_port"
  if [[ ! "$callback_host_port" =~ ^[0-9]+$ ]] || (( callback_host_port < 1 || callback_host_port > 65535 )); then
    fail "$callback_var must be an integer between 1 and 65535"
    continue
  fi
  for existing_port in "${mapped_host_ports[@]}"; do
    if [[ "$existing_port" == "$callback_host_port" ]]; then
      fail "host port $callback_host_port is mapped more than once; adjust CLI_PROXY_HOST_PORT_* values"
    fi
  done
  mapped_host_ports+=("$callback_host_port")
done

management_password="$(dotenv_value MANAGEMENT_PASSWORD)"
if [[ -n "$management_password" ]]; then
  pass "MANAGEMENT_PASSWORD is supplied to the container"
elif [[ -f "$config_path" ]] && awk '
  function trim(s) {
    sub(/^[[:space:]]+/, "", s)
    sub(/[[:space:]]+$/, "", s)
    return s
  }
  $0 ~ /^remote-management:/ { in_section=1; next }
  in_section && $0 ~ /^[^[:space:]]/ { in_section=0 }
  in_section && $0 ~ /^[[:space:]]+secret-key:/ {
    value = $0
    sub(/^[^:]*:[[:space:]]*/, "", value)
    sub(/[[:space:]]+#.*$/, "", value)
    value = trim(value)
    double_quote = sprintf("%c", 34)
    single_quote = sprintf("%c", 39)
    first = substr(value, 1, 1)
    last = substr(value, length(value), 1)
    if (length(value) >= 2 && ((first == double_quote && last == double_quote) || (first == single_quote && last == single_quote))) {
      value = substr(value, 2, length(value) - 2)
    }
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
    if grep -Eq "container_name:[[:space:]]+$container_name$" "$rendered_config"; then
      pass "Compose uses container name $container_name"
    else
      fail "Compose container name does not match CLI_PROXY_CONTAINER_NAME ($container_name)"
    fi
    if awk -v port="$host_port" '
      /^[[:space:]]+target:[[:space:]]*8317[[:space:]]*$/ {
        target=1
        next
      }
      target && /^[[:space:]]+published:/ {
        value=$0
        sub(/^[^:]*:[[:space:]]*/, "", value)
        gsub(/[[:space:]]/, "", value)
        gsub(/"/, "", value)
        if (value == port) found=1
        target=0
        next
      }
      target && $0 !~ /^[[:space:]]+/ { target=0 }
      END { exit(found ? 0 : 1) }
    ' "$rendered_config"; then
      pass "Compose maps host port $host_port to container port 8317"
    else
      fail "Compose does not map CLI_PROXY_HOST_PORT $host_port to container port 8317"
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
  provider_url="${provider_url_override:-$(dotenv_value CPA_LICENSE_PREFLIGHT_URL)}"
  provider_body="${provider_body_override:-$(dotenv_value CPA_LICENSE_PREFLIGHT_BODY)}"
  provider_body_file="${provider_body_file_override:-$(dotenv_value CPA_LICENSE_PREFLIGHT_BODY_FILE)}"
  provider_input_valid=1
  if [[ -n "$provider_body_override" && -n "$provider_body_file" ]]; then
    fail "set only one of --provider-body and --provider-body-file"
    provider_input_valid=0
  elif [[ -n "$provider_body_file" ]]; then
    if [[ "$provider_body_file" != /* ]]; then
      provider_body_file="$compose_dir/$provider_body_file"
    fi
    if [[ ! -f "$provider_body_file" ]]; then
      fail "CPA_LICENSE_PREFLIGHT_BODY_FILE does not point to a file: $provider_body_file"
      provider_input_valid=0
    else
      provider_body="$(cat "$provider_body_file")"
    fi
  fi

  # An empty POST to /licenses/grace is not a harmless health check: a valid
  # request can create a one-time grace window. Require an explicit provider
  # dry-run URL instead of silently probing a mutating endpoint.
  if [[ -z "$provider_url" ]]; then
    fail "--provider requires --provider-url or CPA_LICENSE_PREFLIGHT_URL; configure the storefront's non-mutating dry-run endpoint (empty grace probes are disabled)"
    provider_input_valid=0
  else
    case "$provider_url" in
      http://*|https://*) ;;
      *)
        fail "provider preflight URL must use http:// or https://"
        provider_input_valid=0
        ;;
    esac
    if [[ "$provider_url" == *"/licenses/grace"* && "$provider_url" != *"dry_run"* && "$provider_url" != *"dry-run"* && "$provider_url" != *"preflight"* && "$(dotenv_value CPA_LICENSE_PREFLIGHT_ALLOW_MUTATING)" != "1" ]]; then
      fail "provider preflight URL points to the mutating grace endpoint; use a dedicated dry-run/preflight URL"
      provider_input_valid=0
    fi
  fi

  if [[ -z "$provider_body" ]]; then
    fail "--provider requires a complete JSON body via --provider-body or CPA_LICENSE_PREFLIGHT_BODY(_FILE)"
    provider_input_valid=0
  elif ! command -v python3 >/dev/null 2>&1; then
    fail "python3 is required to validate the provider preflight JSON body"
    provider_input_valid=0
  elif ! python3 - "$provider_body" <<'PY'
import json
import sys

try:
    value = json.loads(sys.argv[1])
except Exception as exc:
    raise SystemExit(f"invalid JSON: {exc}")
if not isinstance(value, dict):
    raise SystemExit("request body must be a JSON object")
PY
  then
    fail "provider preflight body is not a valid JSON object"
    provider_input_valid=0
  fi

  # Prefer the file-backed secret when it is non-empty. This mirrors the
  # Compose behavior while keeping the secret out of the checker's output.
  provider_secret="$direct_client_secret"
  if [[ -f "$secret_host_path" && -s "$secret_host_path" ]]; then
    provider_secret="$(cat "$secret_host_path")"
  fi
  provider_secret="$(printf '%s' "$provider_secret" | sed 's/[[:space:]]*$//')"

  if ! command -v curl >/dev/null 2>&1; then
    fail "curl is required for --provider"
  elif [[ "$provider_input_valid" == "1" && -n "$provider_url" && -n "$provider_body" ]]; then
    provider_response="$(mktemp "${TMPDIR:-/tmp}/cpa-provider-preflight.XXXXXX")"
    provider_args=(
      -sS -o "$provider_response" -w '%{http_code}'
      --connect-timeout 5 --max-time 10
      -X POST "$provider_url"
      -H 'Accept: application/json'
      -H 'Content-Type: application/json'
      -H 'X-CPA-License-Preflight: 1'
      --data-binary "$provider_body"
    )
    if [[ -n "$client_id" ]]; then
      # Match the runtime licensing client and storefront guard contract.
      provider_args+=( -H "X-License-Client-ID: $client_id" )
    fi
    if [[ -n "$provider_secret" ]]; then
      provider_args+=( -H "Authorization: Bearer $provider_secret" )
    fi
    if provider_code="$(curl "${provider_args[@]}")"; then
      :
    else
      provider_code="000"
    fi
    rm -f "$provider_response"
    display_url="${provider_url%%\?*}"
    if [[ "$provider_code" =~ ^2[0-9][0-9]$ ]]; then
      pass "storefront authorization preflight succeeded (HTTP $provider_code): $display_url"
    else
      case "$provider_code" in
        400)
          fail "storefront authorization preflight returned HTTP 400 (request body rejected; verify the provider dry-run contract): $display_url"
          ;;
        401|403)
          fail "storefront authorization preflight returned HTTP $provider_code (client credentials rejected or missing): $display_url"
          ;;
        404)
          fail "storefront authorization preflight returned HTTP 404 (dry-run endpoint is missing): $display_url"
          ;;
        405)
          fail "storefront authorization preflight returned HTTP 405 (URL does not accept POST): $display_url"
          ;;
        3??)
          fail "storefront authorization preflight redirected with HTTP $provider_code; redirects are not accepted: $display_url"
          ;;
        5??)
          fail "storefront authorization preflight returned HTTP $provider_code (provider unavailable): $display_url"
          ;;
        000)
          fail "storefront authorization preflight could not reach the provider: $display_url"
          ;;
        *)
          fail "storefront authorization preflight returned unexpected HTTP $provider_code: $display_url"
          ;;
      esac
    fi
  fi
fi

if [[ "$failures" -gt 0 ]]; then
  echo "License deployment check failed: $failures failure(s), $warnings warning(s)." >&2
  exit 1
fi
echo "License deployment check passed: $warnings warning(s)."
