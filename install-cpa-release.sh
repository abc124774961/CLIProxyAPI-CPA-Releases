#!/usr/bin/env bash
# Install a pinned CPA-Pro release on a customer-owned Linux host.
# The script deliberately clones a tag and builds that checkout locally; it
# never follows a mutable `latest` image or branch.
set -Eeuo pipefail

release_repo="${CPA_RELEASE_REPO:-abc124774961/CLIProxyAPI-CPA-Releases}"
release_version="${CPA_RELEASE_VERSION:-v7.2.148-cpa.2}"
install_dir="${CPA_INSTALL_DIR:-${HOME:-/opt}/cpa-pro}"
expected_commit="${CPA_RELEASE_COMMIT:-}"
allow_existing="${CPA_ALLOW_EXISTING:-0}"
# Provider probing is opt-in. The storefront preflight contract requires a
# complete, non-mutating request body; a fresh customer .env has neither, so a
# default probe would reject an otherwise valid installation.
provider_preflight="${CPA_PROVIDER_PREFLIGHT:-0}"
runtime_verify="${CPA_RUNTIME_VERIFY:-1}"
min_free_mb="${CPA_MIN_FREE_MB:-2048}"
host_port="${CLI_PROXY_HOST_PORT:-8317}"
container_name="${CLI_PROXY_CONTAINER_NAME:-cli-proxy-api}"
incoming_dir=""
lock_dir=""
lock_acquired=0
rollback_tag=""
runtime_management_key_file=""
runtime_config_file=""

# Keep all subsequent paths stable when the installer is invoked with a
# relative CPA_INSTALL_DIR; runtime checks run after changing into that dir.
if [[ "$install_dir" != /* ]]; then
  install_dir="$PWD/$install_dir"
fi
lock_dir="${install_dir}.install-lock"

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
info() { printf 'INFO: %s\n' "$*"; }

cleanup() {
  if [[ -n "$incoming_dir" && -d "$incoming_dir" ]]; then
    rm -rf "$incoming_dir"
  fi
  if [[ "$lock_acquired" == "1" ]]; then
    rm -f "$lock_dir/pid"
    rmdir "$lock_dir" 2>/dev/null || true
  fi
  if [[ -n "$runtime_management_key_file" && -f "$runtime_management_key_file" ]]; then
    rm -f "$runtime_management_key_file"
  fi
}
trap cleanup EXIT

# Read one dotenv value without sourcing customer-controlled shell code. An
# explicitly exported process value wins over the install-directory .env, just
# as it does for Docker Compose.
dotenv_value_file() {
  local file="$1"
  local key="$2"
  local value=""
  if [[ -f "$file" ]]; then
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
    ' "$file")"
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

# Resolve a customer path the same way Docker Compose resolves a bind-mount
# source in this checkout: relative values are rooted at the install directory,
# while `~` and `~/...` use the invoking user's home directory. Keep the path
# absolute so post-start checks do not depend on the installer's current cwd.
resolve_install_path() {
  local path="$1"
  local home_dir="${HOME:-}"
  case "$path" in
    "~")
      [[ -n "$home_dir" ]] || die "HOME is required when CLI_PROXY_LICENSE_PATH uses ~"
      path="$home_dir"
      ;;
    "~/"*)
      [[ -n "$home_dir" ]] || die "HOME is required when a deployment path uses ~"
      path="$home_dir/${path#\~/}"
      ;;
    /*)
      ;;
    *)
      path="$install_dir/$path"
      ;;
  esac
  printf '%s' "$path"
}

# Extract the one-line nested management key used by the release config. CPA
# converts plaintext to bcrypt on startup, so this helper is used before the
# first start to hold the original key for the post-start runtime check.
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
    in_section && /^[^[:space:]]/ { in_section = 0 }
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

is_bcrypt_hash() {
  [[ "$1" =~ ^\$2[aby]\$[0-9]{2}\$ ]]
}

port_is_listening() {
  local port="$1"
  if command -v ss >/dev/null 2>&1; then
    ss -H -ltn 2>/dev/null | awk -v port=":$port" '$4 ~ port "$" || $4 ~ port "\\]$" { found=1 } END { exit(found ? 0 : 1) }'
    return $?
  fi
  if command -v lsof >/dev/null 2>&1; then
    lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1
    return $?
  fi
  return 2
}

case "$release_repo" in
  */*) ;;
  *) die "CPA_RELEASE_REPO must be OWNER/REPOSITORY" ;;
esac
if [[ ! "$release_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-cpa\.[0-9]+$ ]]; then
  die "CPA_RELEASE_VERSION must be a pinned tag such as v7.2.148-cpa.2"
fi

command -v git >/dev/null 2>&1 || die "git is required"
command -v docker >/dev/null 2>&1 || die "docker is required"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v python3 >/dev/null 2>&1 || die "python3 is required"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is required"
docker info >/dev/null 2>&1 || die "Docker daemon is not available"
[[ "$(uname -s)" == "Linux" ]] || die "customer installation requires Linux"
case "$(uname -m)" in
  x86_64|amd64|aarch64|arm64) ;;
  *) die "supported architectures are linux/amd64 and linux/arm64" ;;
esac
case "$provider_preflight" in 0|1) ;; *) die "CPA_PROVIDER_PREFLIGHT must be 0 or 1" ;; esac
case "$runtime_verify" in 0|1) ;; *) die "CPA_RUNTIME_VERIFY must be 0 or 1" ;; esac
case "$min_free_mb" in ''|*[!0-9]*) die "CPA_MIN_FREE_MB must be a non-negative integer" ;; esac

parent_dir="$(dirname "$install_dir")"
mkdir -p "$parent_dir"
available_kb="$(df -Pk "$parent_dir" | awk 'NR == 2 { print $4 }')"
if [[ -n "$available_kb" && "$available_kb" -lt $((min_free_mb * 1024)) ]]; then
  die "less than ${min_free_mb} MiB is available under $parent_dir"
fi
if ! mkdir "$lock_dir" 2>/dev/null; then
  die "another CPA installer is using $install_dir (lock: $lock_dir)"
fi
lock_acquired=1
printf '%s\n' "$$" > "$lock_dir/pid"

if [[ -e "$install_dir" ]]; then
  [[ "$allow_existing" == "1" ]] || die "install directory already exists: $install_dir (set CPA_ALLOW_EXISTING=1 only after backing it up)"
  [[ -d "$install_dir/.git" ]] || die "existing install directory is not a Git checkout: $install_dir"
  current_remote="$(git -C "$install_dir" remote get-url origin 2>/dev/null || true)"
  [[ "$current_remote" == "https://github.com/$release_repo.git" || "$current_remote" == "https://github.com/$release_repo" ]] || die "existing checkout remote does not match $release_repo"
  git -C "$install_dir" diff --quiet || die "tracked release files have local changes; save or revert them before upgrading"
  git -C "$install_dir" diff --cached --quiet || die "tracked release files have staged changes; save or revert them before upgrading"
  git -C "$install_dir" fetch --force origin "refs/tags/$release_version:refs/tags/$release_version"
  git -C "$install_dir" checkout --detach "$release_version"
else
  incoming_dir="${install_dir}.incoming.$$"
  [[ ! -e "$incoming_dir" ]] || die "temporary install path already exists: $incoming_dir"
  git clone --depth 1 --single-branch --branch "$release_version" "https://github.com/$release_repo.git" "$incoming_dir"
  mv "$incoming_dir" "$install_dir"
  incoming_dir=""
fi

actual_commit="$(git -C "$install_dir" rev-parse HEAD)"
actual_tag="$(git -C "$install_dir" describe --tags --exact-match HEAD 2>/dev/null || true)"
[[ "$actual_tag" == "$release_version" ]] || die "checkout is not exactly the requested release tag ($release_version)"
if [[ -n "$expected_commit" && "$actual_commit" != "$expected_commit" ]]; then
  die "release commit mismatch: expected $expected_commit, got $actual_commit"
fi
info "using $release_repo@$release_version ($actual_commit)"

[[ -f "$install_dir/docker-compose.yml" ]] || die "release is missing docker-compose.yml"
[[ -f "$install_dir/.env.example" ]] || die "release is missing .env.example"
[[ -f "$install_dir/config.example.yaml" ]] || die "release is missing config.example.yaml"
[[ -x "$install_dir/scripts/check-license-deployment.sh" ]] || die "release is missing license deployment checker"
[[ -x "$install_dir/scripts/check-license-runtime.sh" ]] || die "release is missing license runtime checker"
[[ -x "$install_dir/scripts/verify-release-bundle.sh" ]] || die "release is missing bundle verifier"
bash "$install_dir/scripts/verify-release-bundle.sh" "$release_version"

first_run=0
if [[ ! -e "$install_dir/.env" ]]; then
  cp "$install_dir/.env.example" "$install_dir/.env"
  chmod 600 "$install_dir/.env"
  info "created $install_dir/.env from .env.example"
  first_run=1
fi
[[ -f "$install_dir/.env" ]] || die "$install_dir/.env exists but is not a regular file"

# Compose reads this value from .env, so mirror that lookup for the local
# health/runtime checks. The port is deliberately numeric: the container
# always listens on 8317 and only the customer host-side port is configurable.
host_port="$(dotenv_value_file "$install_dir/.env" CLI_PROXY_HOST_PORT)"
[[ -n "$host_port" ]] || host_port="8317"
if [[ ! "$host_port" =~ ^[0-9]+$ ]] || (( host_port < 1 || host_port > 65535 )); then
  die "CLI_PROXY_HOST_PORT must be an integer between 1 and 65535"
fi
runtime_base_url="http://127.0.0.1:${host_port}"

container_name="$(dotenv_value_file "$install_dir/.env" CLI_PROXY_CONTAINER_NAME)"
[[ -n "$container_name" ]] || container_name="cli-proxy-api"
if [[ ! "$container_name" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]]; then
  die "CLI_PROXY_CONTAINER_NAME contains invalid Docker name characters"
fi

# The Compose file publishes several provider callback listeners in addition
# to the main API. Validate all host-side mappings before replacement so a
# customer can move this instance to a free port set instead of discovering a
# collision only after `docker compose up`.
declare -a mapped_host_ports=("$host_port")
for callback_spec in \
  "CLI_PROXY_HOST_PORT_8085:8085" \
  "CLI_PROXY_HOST_PORT_1455:1455" \
  "CLI_PROXY_HOST_PORT_54545:54545" \
  "CLI_PROXY_HOST_PORT_51121:51121" \
  "CLI_PROXY_HOST_PORT_11451:11451"; do
  callback_var="${callback_spec%%:*}"
  callback_container_port="${callback_spec##*:}"
  callback_host_port="$(dotenv_value_file "$install_dir/.env" "$callback_var")"
  [[ -n "$callback_host_port" ]] || callback_host_port="$callback_container_port"
  if [[ ! "$callback_host_port" =~ ^[0-9]+$ ]] || (( callback_host_port < 1 || callback_host_port > 65535 )); then
    die "$callback_var must be an integer between 1 and 65535"
  fi
  for existing_port in "${mapped_host_ports[@]}"; do
    [[ "$existing_port" != "$callback_host_port" ]] || die "host port $callback_host_port is mapped more than once; adjust CLI_PROXY_HOST_PORT_* values"
  done
  mapped_host_ports+=("$callback_host_port")
done

# A fresh install must not take over an unrelated container name. During an
# upgrade, the existing Compose-managed CPA container is expected and will be
# replaced by the service-level `docker compose` commands below.
existing_named_container="$(docker ps -aq --filter "name=^${container_name}$" 2>/dev/null || true)"
if [[ -n "$existing_named_container" ]]; then
  existing_service_label="$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.service" }}' "$existing_named_container" 2>/dev/null || true)"
  if [[ "$existing_service_label" != "cli-proxy-api" ]]; then
    die "Docker container name $container_name is already used by another service; set CLI_PROXY_CONTAINER_NAME to a unique name"
  fi
  info "existing Compose-managed CPA container detected ($container_name); its host ports are allowed for upgrade"
else
  for mapped_port in "${mapped_host_ports[@]}"; do
    if port_is_listening "$mapped_port"; then
      die "host port $mapped_port is already listening; set CLI_PROXY_HOST_PORT[_*] to free ports"
    else
      port_probe_status=$?
      if [[ "$port_probe_status" == "2" ]]; then
        info "could not probe host port $mapped_port (ss/lsof unavailable); Docker Compose will validate it"
      fi
    fi
  done
fi

# The default Compose bind mount expects the configured host path to already
# be a file. If it is absent Docker can create a directory at that path, after
# which CPA starts with an unreadable configuration mount. Resolve custom
# paths exactly as Compose does and initialize the selected file while
# preserving any customer-edited configuration.
config_host_path="$(dotenv_value_file "$install_dir/.env" CLI_PROXY_CONFIG_PATH)"
[[ -n "$config_host_path" ]] || config_host_path="./config.yaml"
config_host_path="$(resolve_install_path "$config_host_path")"
config_parent_dir="$(dirname "$config_host_path")"
mkdir -p "$config_parent_dir"
if [[ ! -e "$config_host_path" ]]; then
  cp "$install_dir/config.example.yaml" "$config_host_path"
  chmod 600 "$config_host_path"
  info "created $config_host_path from config.example.yaml"
fi
[[ -f "$config_host_path" ]] || die "$config_host_path exists but is not a regular file"

# Compose accepts a custom host-side license directory. Resolve it before
# creating state so the identity and signed lease survive container recreation
# even when the directory lives outside the release checkout.
license_state_path="$(dotenv_value_file "$install_dir/.env" CLI_PROXY_LICENSE_PATH)"
[[ -n "$license_state_path" ]] || license_state_path="./data/license"
license_state_path="$(resolve_install_path "$license_state_path")"
mkdir -p "$license_state_path"
chmod 700 "$license_state_path"
[[ -d "$license_state_path" ]] || die "license state path is not a directory: $license_state_path"

mkdir -p "$install_dir/auths" "$install_dir/logs" "$install_dir/plugins" "$install_dir/secrets"
chmod 700 "$install_dir/secrets"
# Compose declares this Docker secret even when the storefront client guard is
# disabled; an empty mode-600 file keeps the stack renderable. A configured
# client secret should be written to this file, never committed to Git.
secret_file="$install_dir/secrets/cpa-license-client-secret"
if [[ ! -e "$secret_file" ]]; then
  : > "$secret_file"
  chmod 600 "$secret_file"
fi
[[ -f "$secret_file" ]] || die "$secret_file exists but is not a regular file"

if [[ "$first_run" == "1" ]]; then
  info "initialized config.yaml, persistent directories, and the Docker secret placeholder"
  info "publisher public keys and storefront paths are already pinned by this release"
  info "fill MANAGEMENT_PASSWORD and the customer CPA_LICENSE_CLIENT_ID/CPA_LICENSE_CLIENT_SECRET values in $install_dir/.env"
  info "then rerun this script with CPA_ALLOW_EXISTING=1"
  exit 2
fi

# The CPA process hashes a plaintext YAML management key in place during
# startup. Preserve a mode-600, short-lived copy before replacing the
# container, so the post-start license check can still authenticate without
# injecting MANAGEMENT_PASSWORD into the container environment (which would
# alter remote-management override semantics). Existing bcrypt values require
# the customer to provide the original key through .env or a checker key file.
if [[ "$runtime_verify" == "1" ]]; then
  runtime_config_file="$(dotenv_value_file "$install_dir/.env" CLI_PROXY_CONFIG_PATH)"
  [[ -n "$runtime_config_file" ]] || runtime_config_file="config.yaml"
  runtime_config_file="$(resolve_install_path "$runtime_config_file")"
  configured_management_key="${MANAGEMENT_PASSWORD:-}"
  if [[ -z "$configured_management_key" ]]; then
    configured_management_key="$(dotenv_value_file "$install_dir/.env" MANAGEMENT_PASSWORD)"
  fi
  if [[ -z "$configured_management_key" ]]; then
    [[ -f "$runtime_config_file" && -r "$runtime_config_file" ]] || die "runtime verification needs a readable config file: $runtime_config_file"
    configured_management_key="$(yaml_management_key "$runtime_config_file")"
    if [[ -z "$configured_management_key" ]]; then
      die "runtime verification needs MANAGEMENT_PASSWORD or remote-management.secret-key in $runtime_config_file"
    fi
    if is_bcrypt_hash "$configured_management_key"; then
      die "remote-management.secret-key in $runtime_config_file is already bcrypt-hashed; set MANAGEMENT_PASSWORD before upgrading or provide the original key to the runtime checker"
    fi
    # Keep the snapshot outside the checkout/build context. Docker builds copy
    # the release tree, so placing this file under install_dir would risk
    # persisting the management secret in an intermediate image layer.
    runtime_management_key_file="$(mktemp "${TMPDIR:-/tmp}/cpa-runtime-management-key.XXXXXX")"
    chmod 600 "$runtime_management_key_file"
    printf '%s\n' "$configured_management_key" > "$runtime_management_key_file"
  fi
fi

cd "$install_dir"
bash scripts/check-license-deployment.sh --env-file .env --compose-file docker-compose.yml
if [[ "$provider_preflight" == "1" ]]; then
  bash scripts/check-license-deployment.sh --env-file .env --compose-file docker-compose.yml --provider
fi

docker compose --env-file .env -f docker-compose.yml config >/dev/null
old_container="$(docker compose --env-file .env -f docker-compose.yml ps -q cli-proxy-api 2>/dev/null || true)"
if [[ -n "$old_container" ]]; then
  old_image_id="$(docker inspect -f '{{.Image}}' "$old_container" 2>/dev/null || true)"
  if [[ -n "$old_image_id" ]]; then
    rollback_tag="local/cli-proxy-api-cpa:rollback-$(date -u +%Y%m%dT%H%M%SZ)"
    docker image tag "$old_image_id" "$rollback_tag"
    info "preserved the previous image as $rollback_tag"
  fi
fi
docker compose --env-file .env -f docker-compose.yml build --pull cli-proxy-api

rollback_release() {
  if [[ -n "$rollback_tag" ]]; then
    info "restoring the previous CPA image $rollback_tag"
    CLI_PROXY_IMAGE="$rollback_tag" CLI_PROXY_PULL_POLICY=never \
      docker compose --env-file .env -f docker-compose.yml up -d --no-deps --no-build --force-recreate cli-proxy-api || true
  else
    docker compose --env-file .env -f docker-compose.yml stop cli-proxy-api >/dev/null 2>&1 || true
  fi
}

if ! docker compose --env-file .env -f docker-compose.yml up -d --no-deps cli-proxy-api; then
  rollback_release
  die "CPA container replacement failed"
fi

healthy=0
for attempt in $(seq 1 30); do
  if curl -fsS --max-time 3 "$runtime_base_url/healthz" >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 2
done
if [[ "$healthy" != "1" ]]; then
  rollback_release
  die "CPA did not become healthy; the previous image was restored when available"
fi

if [[ ! -f "$license_state_path/installation.id" ]]; then
  rollback_release
  die "license state was not persisted at $license_state_path"
fi
if [[ "$runtime_verify" == "1" ]]; then
  runtime_check_args=(--env-file .env --config-file "${runtime_config_file:-config.yaml}" --base-url "$runtime_base_url" --wait-seconds 90)
  if [[ -n "$runtime_management_key_file" ]]; then
    runtime_check_args+=(--management-key-file "$runtime_management_key_file")
  fi
  if ! bash scripts/check-license-runtime.sh "${runtime_check_args[@]}"; then
    rollback_release
    die "runtime license verification failed; the previous image was restored when available"
  fi
fi
info "CPA-Pro $release_version is running on 127.0.0.1:${host_port} (container port 8317)"
info "license state is persisted at $license_state_path"
if [[ -n "$rollback_tag" ]]; then
  info "rollback image retained as $rollback_tag"
fi
info "next check: docker compose --env-file .env logs --tail=100 cli-proxy-api"
