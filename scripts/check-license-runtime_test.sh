#!/usr/bin/env bash

# Focused regression tests for the customer-side runtime checker. The tests
# mock curl and exercise management-key precedence without contacting a CPA
# process or the storefront.

set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
checker="$repo_root/scripts/check-license-runtime.sh"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/cpa-runtime-check-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

mkdir -p "$tmp_dir/bin" "$tmp_dir/config-dir"
cat > "$tmp_dir/bin/curl" <<'CURL'
#!/usr/bin/env bash
set -Eeuo pipefail

args_file="${MOCK_CURL_ARGS_FILE:?}"
printf '%s\n' "$@" >> "$args_file"
out_file=""
url=""
previous=""
for arg in "$@"; do
  if [[ "$previous" == "-o" ]]; then
    out_file="$arg"
  fi
  url="$arg"
  previous="$arg"
done
if [[ "$url" == */healthz ]]; then
  printf '%s' "${MOCK_HEALTH_STATUS:-200}"
  exit 0
fi
if [[ -n "$out_file" && "$out_file" != "/dev/null" ]]; then
  printf '%s' "${MOCK_STATUS_BODY:-{\"allowed\":true,\"configured\":true,\"integrity_valid\":true,\"reason\":\"grace\"}}" > "$out_file"
fi
printf '%s' "${MOCK_STATUS_CODE:-200}"
CURL
chmod 755 "$tmp_dir/bin/curl"

cat > "$tmp_dir/config-dir/config.yaml" <<'EOF_CFG'
remote-management:
  allow-remote: false
  secret-key: "yaml-management-key"
EOF_CFG

run_checker() {
  : > "$tmp_dir/curl.args"
  PATH="$tmp_dir/bin:$PATH" \
    MOCK_CURL_ARGS_FILE="$tmp_dir/curl.args" \
    env -u MANAGEMENT_PASSWORD \
    bash "$checker" "$@"
}

assert_contains() {
  local file="$1"
  local needle="$2"
  grep -F -- "$needle" "$file" >/dev/null || {
    echo "expected $file to contain: $needle" >&2
    cat "$file" >&2
    exit 1
  }
}

assert_fails_with() {
  local expected="$1"
  shift
  local output_file="$tmp_dir/output"
  if "$@" >"$output_file" 2>&1; then
    echo "expected command to fail: $*" >&2
    cat "$output_file" >&2
    exit 1
  fi
  assert_contains "$output_file" "$expected"
}

# A plaintext key in the customer config is accepted when no env/key file is
# available, and the configured custom host port is used for both requests.
run_checker \
  --config-file "$tmp_dir/config-dir/config.yaml" \
  --base-url http://127.0.0.1:18320 \
  --wait-seconds 0 >/dev/null
assert_contains "$tmp_dir/curl.args" "http://127.0.0.1:18320/healthz"
assert_contains "$tmp_dir/curl.args" "Bearer yaml-management-key"
assert_contains "$tmp_dir/curl.args" "http://127.0.0.1:18320/v0/management/license/status"

# MANAGEMENT_PASSWORD in --env-file takes precedence over the YAML key.
cat > "$tmp_dir/env" <<EOF_ENV
MANAGEMENT_PASSWORD=dotenv-management-key
CLI_PROXY_CONFIG_PATH=$tmp_dir/config-dir/config.yaml
EOF_ENV
run_checker --env-file "$tmp_dir/env" --wait-seconds 0 >/dev/null
assert_contains "$tmp_dir/curl.args" "Bearer dotenv-management-key"

# An explicit mode-restricted key file takes precedence over both sources.
printf '%s\n' file-management-key > "$tmp_dir/management.key"
chmod 600 "$tmp_dir/management.key"
run_checker --env-file "$tmp_dir/env" --management-key-file "$tmp_dir/management.key" --wait-seconds 0 >/dev/null
assert_contains "$tmp_dir/curl.args" "Bearer file-management-key"

# Once CPA has rewritten the YAML value as bcrypt, the original key must be
# supplied explicitly instead of sending the hash as a bearer token.
cat > "$tmp_dir/config-dir/hashed.yaml" <<'EOF_HASH'
remote-management:
  secret-key: '$2b$10$012345678901234567890uV5fQ3f4nKzQ9zXQ5aQ0nQwQY5M0q'
EOF_HASH
assert_fails_with "already contains a bcrypt-hashed" \
  run_checker --config-file "$tmp_dir/config-dir/hashed.yaml" --wait-seconds 0
[[ ! -s "$tmp_dir/curl.args" ]] || {
  echo "runtime checker contacted curl despite missing original management key" >&2
  exit 1
}

echo "check-license-runtime tests passed"
