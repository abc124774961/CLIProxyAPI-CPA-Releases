#!/usr/bin/env bash

# Verify a running customer CPA instance through its local health, management,
# license, and optional API canary endpoints. Secrets are read from the process
# environment or a file and are never echoed.

set -Eeuo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/check-license-runtime.sh [options]

Options:
  --base-url URL          CPA base URL (default: http://127.0.0.1:8317)
  --env-file PATH         Read MANAGEMENT_PASSWORD from a dotenv file when the
                          process environment does not provide it
  --config-file PATH      Read remote-management.secret-key from this YAML file
                          when no environment or key-file secret is provided
  --management-key-file PATH
                          Read the management key from a mode-restricted file
  --api-key-file PATH     Read a downstream API key for /v1/models canary
  --wait-seconds N        Retry license status for N seconds (default: 30)
  -h, --help              Show this help

The MANAGEMENT_PASSWORD environment variable supplies the management key when
--management-key-file is omitted. If neither is present, the script reads the
plaintext `remote-management.secret-key` from the YAML file selected by
--config-file, CLI_PROXY_CONFIG_PATH in --env-file, or config.yaml. A bcrypt
hash persisted by CPA cannot be used as a bearer key; provide the original key
through MANAGEMENT_PASSWORD or --management-key-file in that case. The API
canary is skipped when --api-key-file is omitted.
USAGE
}

base_url="http://127.0.0.1:8317"
env_file=""
config_file=""
management_key_file=""
api_key_file=""
wait_seconds=30

while [[ $# -gt 0 ]]; do
  case "$1" in
    --base-url)
      [[ $# -ge 2 ]] || { echo "--base-url requires a URL" >&2; exit 2; }
      base_url="$2"
      shift 2
      ;;
    --env-file)
      [[ $# -ge 2 ]] || { echo "--env-file requires a path" >&2; exit 2; }
      env_file="$2"
      shift 2
      ;;
    --config-file)
      [[ $# -ge 2 ]] || { echo "--config-file requires a path" >&2; exit 2; }
      config_file="$2"
      shift 2
      ;;
    --management-key-file)
      [[ $# -ge 2 ]] || { echo "--management-key-file requires a path" >&2; exit 2; }
      management_key_file="$2"
      shift 2
      ;;
    --api-key-file)
      [[ $# -ge 2 ]] || { echo "--api-key-file requires a path" >&2; exit 2; }
      api_key_file="$2"
      shift 2
      ;;
    --wait-seconds)
      [[ $# -ge 2 ]] || { echo "--wait-seconds requires a number" >&2; exit 2; }
      wait_seconds="$2"
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

case "$wait_seconds" in
  ''|*[!0-9]*) echo "--wait-seconds must be a non-negative integer" >&2; exit 2 ;;
esac

command -v curl >/dev/null 2>&1 || { echo "FAIL: curl is required" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "FAIL: python3 is required" >&2; exit 1; }

base_url="${base_url%/}"

caller_dir="$PWD"
if [[ -n "$env_file" && "$env_file" != /* ]]; then
  env_file="$caller_dir/$env_file"
fi
env_dir="$caller_dir"
if [[ -f "$env_file" ]]; then
  env_dir="$(cd "$(dirname "$env_file")" && pwd)"
fi

# Read one dotenv value without sourcing customer-controlled shell code. This
# mirrors Compose's precedence: an exported process value wins over --env-file.
dotenv_value() {
  local key="$1"
  local value=""
  if [[ -f "$env_file" ]]; then
    value="$(awk -v key="$key" '
      /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
      {
        line = $0
        sub(/^[[:space:]]*export[[:space:]]+/, "", line)
        if (line ~ ("^" key "=")) {
          sub(/^[^=]*=/, "", line)
          print line
          exit
        }
      }
    ' "$env_file")"
  fi
  if [[ -n "${!key+x}" ]]; then
    value="${!key}"
  fi
  value="${value#\"}"
  value="${value%\"}"
  value="${value#\'}"
  value="${value%\'}"
  printf '%s' "$(printf '%s' "$value" | sed 's/[[:space:]]*$//')"
}

resolve_path() {
  local path="$1"
  case "$path" in
    "~")
      path="$HOME"
      ;;
    "~/"*)
      path="$HOME/${path#\~/}"
      ;;
    /*)
      ;;
    *)
      path="$env_dir/$path"
      ;;
  esac
  printf '%s' "$path"
}

# Extract the simple scalar used by the CPA config. The release config keeps
# this key on one indented line; comments and matching quotes are handled so a
# normal generated key is read without requiring PyYAML or yq on the host.
yaml_management_key() {
  local path="$1"
  [[ -f "$path" && -r "$path" ]] || return 0
  awk '
    function trim(s) {
      sub(/^[[:space:]]+/, "", s)
      sub(/[[:space:]]+$/, "", s)
      return s
    }
    /^[[:space:]]*remote-management:[[:space:]]*(#.*)?$/ {
      in_section = 1
      next
    }
    in_section && /^[^[:space:]]/ {
      in_section = 0
    }
    in_section && /^[[:space:]]+secret-key:[[:space:]]*/ {
      value = $0
      sub(/^[^:]*:[[:space:]]*/, "", value)
      value = trim(value)
      if (value ~ /^"[^"]*"[[:space:]]*(#.*)?$/) {
        sub(/[[:space:]]+#.*$/, "", value)
        value = substr(value, 2, length(value) - 2)
      } else if (value ~ /^\047[^\047]*\047[[:space:]]*(#.*)?$/) {
        sub(/[[:space:]]+#.*$/, "", value)
        value = substr(value, 2, length(value) - 2)
      } else {
        sub(/[[:space:]]+#.*$/, "", value)
        value = trim(value)
      }
      print value
      exit
    }
  ' "$path"
}

management_key="${MANAGEMENT_PASSWORD:-}"
if [[ -z "$management_key" && -n "$env_file" ]]; then
  [[ -r "$env_file" ]] || { echo "FAIL: environment file is not readable" >&2; exit 1; }
  management_key="$(dotenv_value MANAGEMENT_PASSWORD)"
fi
if [[ -n "$management_key_file" ]]; then
  management_key_file="$(resolve_path "$management_key_file")"
  [[ -r "$management_key_file" && -f "$management_key_file" ]] || { echo "FAIL: management key file is not readable" >&2; exit 1; }
  management_key="$(cat "$management_key_file")"
fi
management_key="$(printf '%s' "$management_key" | sed 's/[[:space:]]*$//')"

if [[ -z "$config_file" ]]; then
  config_file="$(dotenv_value CLI_PROXY_CONFIG_PATH)"
  [[ -n "$config_file" ]] || config_file="config.yaml"
fi
config_file="$(resolve_path "$config_file")"
config_key_was_hashed=0
if [[ -z "$management_key" ]]; then
  config_management_key="$(yaml_management_key "$config_file")"
  if [[ -n "$config_management_key" ]]; then
    # CPA hashes a plaintext YAML key in place during startup. A persisted
    # bcrypt value is intentionally not treated as the original secret.
    if [[ "$config_management_key" =~ ^\$2[aby]\$[0-9]{2}\$ ]]; then
      config_key_was_hashed=1
    else
      management_key="$config_management_key"
    fi
  fi
fi
if [[ -z "$management_key" ]]; then
  if [[ "$config_key_was_hashed" == "1" ]]; then
    echo "FAIL: config file already contains a bcrypt-hashed remote-management.secret-key; provide MANAGEMENT_PASSWORD or --management-key-file with the original key" >&2
  else
    echo "FAIL: MANAGEMENT_PASSWORD, --management-key-file, or plaintext remote-management.secret-key in $config_file is required" >&2
  fi
  exit 1
fi

api_key=""
if [[ -n "$api_key_file" ]]; then
  [[ -r "$api_key_file" ]] || { echo "FAIL: API key file is not readable" >&2; exit 1; }
  api_key="$(cat "$api_key_file")"
  api_key="$(printf '%s' "$api_key" | sed 's/[[:space:]]*$//')"
  [[ -n "$api_key" ]] || { echo "FAIL: API key file is empty" >&2; exit 1; }
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/cpa-runtime-check.XXXXXX")"
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT
chmod 700 "$tmp_dir"

pass() { echo "OK: $*"; }
fail() { echo "FAIL: $*" >&2; }

health_code="$(curl -sS -o /dev/null -w '%{http_code}' --connect-timeout 5 --max-time 10 "$base_url/healthz" || printf '000')"
if [[ "$health_code" != 2?? ]]; then
  fail "health endpoint returned HTTP $health_code"
  exit 1
fi
pass "health endpoint returned HTTP $health_code"

status_file="$tmp_dir/license-status.json"
status_code=000
status_reason=""
status_allowed="false"
status_configured="false"
status_integrity="false"
status_last_error=""

poll_deadline=$((SECONDS + wait_seconds))
while :; do
  status_code="$(curl -sS -o "$status_file" -w '%{http_code}' \
    --connect-timeout 5 --max-time 10 \
    -H "Authorization: Bearer $management_key" \
    "$base_url/v0/management/license/status" || printf '000')"
  if [[ "$status_code" == 2?? ]]; then
    status_values="$(python3 - "$status_file" <<'PY'
import json
import sys

try:
    with open(sys.argv[1], encoding="utf-8") as fh:
        payload = json.load(fh)
except Exception:
    print("false\tfalse\tfalse\tinvalid_json\t")
    raise SystemExit(0)
print(
    "{}\t{}\t{}\t{}\t{}".format(
        str(payload.get("allowed") is True).lower(),
        str(payload.get("configured") is True).lower(),
        str(payload.get("integrity_valid") is True).lower(),
        str(payload.get("reason", "")),
        str(payload.get("last_refresh_error", "")),
    )
)
PY
)"
    IFS=$'\t' read -r status_allowed status_configured status_integrity status_reason status_last_error <<<"$status_values"
    if [[ "$status_configured" == "true" && "$status_allowed" == "true" && ( "$status_reason" == "active" || "$status_reason" == "grace" || "$status_reason" == "expiry_grace" ) ]]; then
      if [[ "$status_integrity" == "true" ]]; then
        pass "license status is allowed (reason=$status_reason)"
        break
      fi
      fail "license integrity check is not valid"
      exit 1
    fi
  fi
  if (( SECONDS >= poll_deadline )); then
    if [[ "$status_code" != 2?? ]]; then
      fail "license status endpoint returned HTTP $status_code"
    else
      fail "license status is not allowed (reason=${status_reason:-unknown}, last_refresh_error=${status_last_error:-none})"
    fi
    exit 1
  fi
  sleep 1
done

if [[ -n "$api_key" ]]; then
  api_code="$(curl -sS -o /dev/null -w '%{http_code}' --connect-timeout 5 --max-time 20 \
    -H "Authorization: Bearer $api_key" \
    "$base_url/v1/models" || printf '000')"
  if [[ "$api_code" != 2?? ]]; then
    fail "API canary /v1/models returned HTTP $api_code"
    exit 1
  fi
  pass "API canary /v1/models returned HTTP $api_code"
else
  echo "WARN: API canary skipped; pass --api-key-file to verify a downstream key" >&2
fi

echo "Runtime license check passed."
