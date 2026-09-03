#!/usr/bin/env bash

# Focused regression tests for the customer-side deployment checker. The tests
# use the real Docker Compose renderer and a mocked curl so no storefront call
# is made and no license state is created.

set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
checker="$repo_root/scripts/check-license-deployment.sh"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/cpa-deployment-check-test.XXXXXX")"
tmp_dir="$(cd "$tmp_dir" && pwd)"
trap 'rm -rf "$tmp_dir"' EXIT

mkdir -p "$tmp_dir/bin" "$tmp_dir/auths" "$tmp_dir/logs" "$tmp_dir/plugins" "$tmp_dir/license" "$tmp_dir/secrets"
cp "$repo_root/docker-compose.yml" "$tmp_dir/docker-compose.yml"
cp "$repo_root/docker-compose.cluster.yml" "$tmp_dir/docker-compose.cluster.yml"
printf '%s' 'file-backed-preflight-secret' > "$tmp_dir/secrets/client-secret"
chmod 600 "$tmp_dir/secrets/client-secret"

cat > "$tmp_dir/bin/curl" <<'CURL'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "$@" > "${MOCK_CURL_ARGS_FILE:?}"
if [[ "${MOCK_CURL_EXIT:-0}" != "0" ]]; then
  exit "$MOCK_CURL_EXIT"
fi
printf '%s' "${MOCK_CURL_STATUS:-204}"
CURL
chmod 755 "$tmp_dir/bin/curl"

cat > "$tmp_dir/base.env" <<EOF
MANAGEMENT_PASSWORD=management-test
CPA_LICENSE_PUBLIC_KEY=kJhDRBpfneFdURvPXwiGW3XAmPrd2HVVORfHzP-eYTg
CPA_LICENSE_PLUGIN_PUBLIC_KEY=OHRHVVIlFC34K-5AQUkOPcZLeiSpeX_n_VPbrH3agXQ
CPA_LICENSE_PROVIDER=shop666
CPA_LICENSE_PRODUCT_CODE=CPA
CPA_LICENSE_API_BASE_URL=https://p.666ttt.net/api/storefront
CPA_LICENSE_CLIENT_ID=client-test
CPA_LICENSE_CLIENT_SECRET=
CPA_LICENSE_CLIENT_SECRET_HOST_PATH=$tmp_dir/secrets/client-secret
CPA_LICENSE_STATE_DIR=/CLIProxyAPI/data/license
CPA_LICENSE_SHOP_AUTH_URL=https://p.666ttt.net/shop/?authorize=cpa
CPA_LICENSE_SHOP_EXCHANGE_PATH=/licenses/exchange
CPA_LICENSE_ACTIVATE_PATH=/licenses/activate
CPA_LICENSE_REFRESH_PATH=/licenses/refresh
CPA_LICENSE_VERIFY_PATH=/licenses/verify
CPA_LICENSE_GRACE_PATH=/licenses/grace
CPA_LICENSE_REFRESH_INTERVAL=10m
CPA_LICENSE_GRACE_PERIOD=6h
CLI_PROXY_CONFIG_PATH=$repo_root/config.example.yaml
CLI_PROXY_AUTH_PATH=$tmp_dir/auths
CLI_PROXY_LOG_PATH=$tmp_dir/logs
CLI_PROXY_PLUGIN_PATH=$tmp_dir/plugins
CLI_PROXY_LICENSE_PATH=$tmp_dir/license
CLI_PROXY_PULL_POLICY=build
EOF

run_checker() {
  local env_file="$1"
  shift
  run_checker_with_compose "$env_file" "$repo_root/docker-compose.yml" "$@"
}

run_checker_with_compose() {
  local env_file="$1"
  local compose_file="$2"
  shift 2
  PATH="$tmp_dir/bin:$PATH" \
    MOCK_CURL_ARGS_FILE="$tmp_dir/curl.args" \
    env -u CPA_LICENSE_PREFLIGHT_URL -u CPA_LICENSE_PREFLIGHT_BODY -u CPA_LICENSE_PREFLIGHT_BODY_FILE \
    bash "$checker" --env-file "$env_file" --compose-file "$compose_file" "$@"
}

assert_contains() {
  local file="$1"
  local needle="$2"
  grep -F -- "$needle" "$file" >/dev/null || {
    echo "expected $file to contain: $needle" >&2
    exit 1
  }
}

# Existing customers may keep publisher keys in config.yaml rather than .env.
# The checker should accept that layout while still rendering the same Compose
# contract.
cat > "$tmp_dir/config-with-key.yaml" <<'EOF'
license:
  public-key: "kJhDRBpfneFdURvPXwiGW3XAmPrd2HVVORfHzP-eYTg"
  plugin-public-key: "OHRHVVIlFC34K-5AQUkOPcZLeiSpeX_n_VPbrH3agXQ"
remote-management:
  secret-key: "management-test"
EOF
cat "$tmp_dir/base.env" | sed \
  -e 's/^MANAGEMENT_PASSWORD=.*/MANAGEMENT_PASSWORD=/' \
  -e 's/^CPA_LICENSE_PUBLIC_KEY=.*/CPA_LICENSE_PUBLIC_KEY=/' \
  -e 's/^CPA_LICENSE_PLUGIN_PUBLIC_KEY=.*/CPA_LICENSE_PLUGIN_PUBLIC_KEY=/' \
  -e "s#^CLI_PROXY_CONFIG_PATH=.*#CLI_PROXY_CONFIG_PATH=$tmp_dir/config-with-key.yaml#" \
  > "$tmp_dir/config-key.env"
config_key_output="$tmp_dir/config-key.output"
run_checker "$tmp_dir/config-key.env" >"$config_key_output"
assert_contains "$config_key_output" "CPA_LICENSE_PUBLIC_KEY decodes to an Ed25519 public key (source: config.yaml)"
assert_contains "$config_key_output" "CPA_LICENSE_PLUGIN_PUBLIC_KEY decodes to an Ed25519 public key (source: config.yaml)"

# Relative config paths must be resolved from the Compose file directory for
# both public-key and management-key checks. This mirrors Docker Compose when
# the customer keeps a copied compose file in a separate deployment folder.
mkdir -p "$tmp_dir/relative-compose"
cp "$tmp_dir/docker-compose.yml" "$tmp_dir/relative-compose/docker-compose.yml"
cp "$tmp_dir/config-with-key.yaml" "$tmp_dir/relative-compose/config.yaml"
cat "$tmp_dir/base.env" | sed \
  -e 's/^MANAGEMENT_PASSWORD=.*/MANAGEMENT_PASSWORD=/' \
  -e 's/^CPA_LICENSE_PUBLIC_KEY=.*/CPA_LICENSE_PUBLIC_KEY=/' \
  -e 's/^CPA_LICENSE_PLUGIN_PUBLIC_KEY=.*/CPA_LICENSE_PLUGIN_PUBLIC_KEY=/' \
  -e 's#^CLI_PROXY_CONFIG_PATH=.*#CLI_PROXY_CONFIG_PATH=config.yaml#' \
  > "$tmp_dir/relative-compose.env"
relative_output="$tmp_dir/relative-compose.output"
run_checker_with_compose "$tmp_dir/relative-compose.env" "$tmp_dir/relative-compose/docker-compose.yml" >"$relative_output"
assert_contains "$relative_output" "CPA_LICENSE_PUBLIC_KEY decodes to an Ed25519 public key (source: config.yaml)"
assert_contains "$relative_output" "management key is configured in config.yaml"

assert_license_mount_source() {
  local env_file="$1"
  local expected="$2"
  local rendered source
  rendered="$(docker compose --env-file "$env_file" -f "$tmp_dir/docker-compose.yml" config)"
  source="$(printf '%s\n' "$rendered" | awk '
    /source:[[:space:]]/ { candidate=$0 }
    /target:[[:space:]]*\/CLIProxyAPI\/data\/license[[:space:]]*$/ {
      sub(/^[[:space:]]*source:[[:space:]]*/, "", candidate)
      print candidate
      exit
    }
  ')"
  [[ "$source" == "$expected" ]] || {
    echo "expected license mount source $expected, got $source" >&2
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

# Compose and the installer must agree on how the host-side license state path
# is rooted. Relative values are under the checkout/installation directory,
# while tilde and absolute values remain host-absolute paths.
cat "$tmp_dir/base.env" > "$tmp_dir/license-relative.env"
printf '%s\n' 'CLI_PROXY_LICENSE_PATH=state/relative' >> "$tmp_dir/license-relative.env"
assert_license_mount_source "$tmp_dir/license-relative.env" "$tmp_dir/state/relative"

cat "$tmp_dir/base.env" > "$tmp_dir/license-home.env"
printf '%s\n' 'CLI_PROXY_LICENSE_PATH=~/state/home' >> "$tmp_dir/license-home.env"
assert_license_mount_source "$tmp_dir/license-home.env" "$HOME/state/home"

cat "$tmp_dir/base.env" > "$tmp_dir/license-absolute.env"
printf 'CLI_PROXY_LICENSE_PATH=%s\n' "$tmp_dir/state/absolute" >> "$tmp_dir/license-absolute.env"
assert_license_mount_source "$tmp_dir/license-absolute.env" "$tmp_dir/state/absolute"

# The cluster Compose file has its own default container name. A copied
# customer env may omit CLI_PROXY_CONTAINER_NAME, so the checker must mirror
# that default instead of reporting a false conflict.
cat "$tmp_dir/base.env" > "$tmp_dir/cluster-default.env"
cat >> "$tmp_dir/cluster-default.env" <<EOF
HOME_JWT=cluster-jwt
CLI_PROXY_HOME_PATH=$tmp_dir/home
EOF
mkdir -p "$tmp_dir/home"
run_checker_with_compose "$tmp_dir/cluster-default.env" "$tmp_dir/docker-compose.cluster.yml" >/dev/null

# Keep the installer’s post-start assertion tied to the resolved path rather
# than the historical ./data/license default. This guards the customer upgrade
# path where state is intentionally stored outside the release checkout. The
# assertion now rolls back before failing, so match the path and rollback
# guard independently instead of requiring the old single-line expression.
grep -F 'license_state_path/installation.id' "$repo_root/install-cpa-release.sh" >/dev/null || {
  echo "installer does not check installation.id under the resolved license path" >&2
  exit 1
}
grep -F 'rollback_release' "$repo_root/install-cpa-release.sh" >/dev/null || {
  echo "installer does not roll back when license state is missing" >&2
  exit 1
}
if grep -F '[[ -f "$install_dir/data/license/installation.id" ]]' "$repo_root/install-cpa-release.sh" >/dev/null; then
  echo "installer still hard-codes the default license state path" >&2
  exit 1
fi

# Without an explicit dry-run/preflight URL, --provider must fail before making
# a request rather than POSTing an empty body to the mutating grace endpoint.
rm -f "$tmp_dir/curl.args"
assert_fails_with "requires --provider-url or CPA_LICENSE_PREFLIGHT_URL" \
  run_checker "$tmp_dir/base.env" --provider
[[ ! -e "$tmp_dir/curl.args" ]] || {
  echo "provider curl was invoked without a configured preflight URL" >&2
  exit 1
}

# A complete request to an explicit dry-run URL succeeds only for HTTP 2xx and
# carries both the client identity and the file-backed bearer secret.
cat "$tmp_dir/base.env" > "$tmp_dir/success.env"
cat >> "$tmp_dir/success.env" <<'EOF'
CPA_LICENSE_PREFLIGHT_URL=https://p.666ttt.net/api/storefront/licenses/preflight?dry_run=1
CPA_LICENSE_PREFLIGHT_BODY={"product":"CPA","instance_id":"preflight-test","dry_run":true}
EOF
MOCK_CURL_STATUS=204 run_checker "$tmp_dir/success.env" --provider >/dev/null
assert_contains "$tmp_dir/curl.args" "https://p.666ttt.net/api/storefront/licenses/preflight?dry_run=1"
assert_contains "$tmp_dir/curl.args" '{"product":"CPA","instance_id":"preflight-test","dry_run":true}'
assert_contains "$tmp_dir/curl.args" "Bearer file-backed-preflight-secret"
assert_contains "$tmp_dir/curl.args" "X-License-Client-ID: client-test"

# Any non-2xx response is a failure; in particular credentials and malformed
# request responses must not be reported as an endpoint-success signal.
MOCK_CURL_STATUS=401 assert_fails_with "client credentials rejected or missing" \
  run_checker "$tmp_dir/success.env" --provider
MOCK_CURL_STATUS=400 assert_fails_with "request body rejected" \
  run_checker "$tmp_dir/success.env" --provider
MOCK_CURL_STATUS=404 assert_fails_with "dry-run endpoint is missing" \
  run_checker "$tmp_dir/success.env" --provider
MOCK_CURL_STATUS=500 assert_fails_with "provider unavailable" \
  run_checker "$tmp_dir/success.env" --provider

# A body file is accepted as the alternative to an inline JSON value.
printf '%s' '{"product":"CPA","instance_id":"file-body","dry_run":true}' > "$tmp_dir/preflight.json"
cat "$tmp_dir/base.env" > "$tmp_dir/file-body.env"
cat >> "$tmp_dir/file-body.env" <<EOF
CPA_LICENSE_PREFLIGHT_URL=https://preflight.example.test/cpa
CPA_LICENSE_PREFLIGHT_BODY_FILE=$tmp_dir/preflight.json
EOF
MOCK_CURL_STATUS=200 run_checker "$tmp_dir/file-body.env" --provider >/dev/null
assert_contains "$tmp_dir/curl.args" '{"product":"CPA","instance_id":"file-body","dry_run":true}'

# Invalid JSON is rejected locally and never sent.
cat "$tmp_dir/base.env" > "$tmp_dir/invalid.env"
cat >> "$tmp_dir/invalid.env" <<'EOF'
CPA_LICENSE_PREFLIGHT_URL=https://preflight.example.test/cpa
CPA_LICENSE_PREFLIGHT_BODY={not-json}
EOF
rm -f "$tmp_dir/curl.args"
assert_fails_with "not a valid JSON object" run_checker "$tmp_dir/invalid.env" --provider
[[ ! -e "$tmp_dir/curl.args" ]] || {
  echo "invalid provider JSON was sent to curl" >&2
  exit 1
}

echo "check-license-deployment preflight tests passed"
