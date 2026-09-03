#!/usr/bin/env bash
# Install a pinned CPA-Pro release on a customer-owned Linux host.
# The script deliberately clones a tag and builds that checkout locally; it
# never follows a mutable `latest` image or branch.
set -Eeuo pipefail

release_repo="${CPA_RELEASE_REPO:-abc124774961/CLIProxyAPI-CPA-Releases}"
release_version="${CPA_RELEASE_VERSION:-v7.2.148-cpa.1}"
install_dir="${CPA_INSTALL_DIR:-${HOME:-/opt}/cpa-pro}"
expected_commit="${CPA_RELEASE_COMMIT:-}"
allow_existing="${CPA_ALLOW_EXISTING:-0}"
provider_preflight="${CPA_PROVIDER_PREFLIGHT:-1}"
runtime_verify="${CPA_RUNTIME_VERIFY:-1}"
min_free_mb="${CPA_MIN_FREE_MB:-2048}"
incoming_dir=""
lock_dir="${install_dir}.install-lock"
lock_acquired=0
rollback_tag=""

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
}
trap cleanup EXIT

case "$release_repo" in
  */*) ;;
  *) die "CPA_RELEASE_REPO must be OWNER/REPOSITORY" ;;
esac
if [[ ! "$release_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-cpa\.[0-9]+$ ]]; then
  die "CPA_RELEASE_VERSION must be a pinned tag such as v7.2.148-cpa.1"
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

# The default Compose bind mount expects config.yaml to already be a file. If
# it is absent Docker can create a directory at that path, after which CPA
# starts with an unreadable configuration mount. Initialize it before the
# first Compose render, while preserving any customer-edited file.
if [[ ! -e "$install_dir/config.yaml" ]]; then
  cp "$install_dir/config.example.yaml" "$install_dir/config.yaml"
  chmod 600 "$install_dir/config.yaml"
  info "created $install_dir/config.yaml from config.example.yaml"
fi
[[ -f "$install_dir/config.yaml" ]] || die "$install_dir/config.yaml exists but is not a regular file"

mkdir -p "$install_dir/auths" "$install_dir/logs" "$install_dir/plugins" "$install_dir/data/license" "$install_dir/secrets"
chmod 700 "$install_dir/data/license" "$install_dir/secrets"
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
  if curl -fsS --max-time 3 http://127.0.0.1:8317/healthz >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 2
done
if [[ "$healthy" != "1" ]]; then
  rollback_release
  die "CPA did not become healthy; the previous image was restored when available"
fi

[[ -f "$install_dir/data/license/installation.id" ]] || die "license state was not persisted at $install_dir/data/license"
if [[ "$runtime_verify" == "1" ]]; then
  if ! bash scripts/check-license-runtime.sh --env-file .env --wait-seconds 90; then
    rollback_release
    die "runtime license verification failed; the previous image was restored when available"
  fi
fi
info "CPA-Pro $release_version is running on 127.0.0.1:8317"
info "license state is persisted at $install_dir/data/license"
if [[ -n "$rollback_tag" ]]; then
  info "rollback image retained as $rollback_tag"
fi
info "next check: docker compose --env-file .env logs --tail=100 cli-proxy-api"
