#!/usr/bin/env bash
# Import locally verified OCI archives without rebuilding private component sources.
set -Eeuo pipefail
export LC_ALL=C
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="${1:-${GITHUB_REF_NAME:-}}"
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-cpa\.[0-9]+(-beta\.[1-9][0-9]*)?$ ]] || {
  printf 'ERROR: invalid public release version\n' >&2
  exit 1
}
if [[ "$version" != *-beta.* ]]; then
  printf 'Stable release: prebuilt beta import is not used.\n'
  exit 0
fi
for command in gh skopeo python3 sha256sum; do
  command -v "$command" >/dev/null 2>&1 || { printf 'ERROR: %s is required\n' "$command" >&2; exit 1; }
done
[[ "${GITHUB_REPOSITORY:-}" == "abc124774961/CLIProxyAPI-CPA-Releases" ]] || {
  printf 'ERROR: unexpected publishing repository\n' >&2
  exit 1
}
: "${GITHUB_ACTOR:?GITHUB_ACTOR is required}"
: "${GH_TOKEN:?GH_TOKEN is required}"
work_dir="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/cpa-beta-images.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT
export REGISTRY_AUTH_FILE="$work_dir/registry-auth.json"
validator="$repo_root/scripts/validate-beta-images.py"
python3 "$validator" plan "$repo_root/beta-image-imports.json" "$repo_root/release-manifest.json" "$version" > "$work_dir/plan.tsv"
printf '%s' "$GH_TOKEN" | skopeo login --username "$GITHUB_ACTOR" --password-stdin ghcr.io >/dev/null

# Fail closed on permission, transport, and rate-limit errors; only an explicit
# registry manifest-not-found response means that creating a tag is appropriate.
remote_exists() {
  if skopeo inspect --raw "docker://$image" > "$work_dir/remote.json" 2> "$work_dir/remote.err"; then
    python3 "$validator" verify "$work_dir/remote.json" "$digest" "$amd64" "$arm64" || return 2
    return 0
  fi
  if ! grep -Eiq 'manifest unknown|MANIFEST_UNKNOWN|NAME_UNKNOWN' "$work_dir/remote.err"; then
    printf 'ERROR: registry lookup failed for %s; publication stopped\n' "$image" >&2
    return 2
  fi
  return 1
}

verify_labels() {
  local reference="$1"
  local arch=""
  for arch in amd64 arm64; do
    skopeo inspect --override-os linux --override-arch "$arch" "$reference" > "$work_dir/labels.json"
    python3 "$validator" labels "$work_dir/labels.json" "$revision" "${image##*:}"
  done
}

while IFS=$'\t' read -r image archive checksum digest revision amd64 arm64; do
  if remote_exists; then
    verify_labels "docker://$image"
    printf 'Verified existing immutable beta image: %s\n' "$image"
    continue
  else
    result=$?
    [[ "$result" == 1 ]] || exit "$result"
  fi
  gh release download "$version" --repo "$GITHUB_REPOSITORY" --pattern "$archive" --dir "$work_dir"
  (cd "$work_dir" && printf '%s  %s\n' "$checksum" "$archive" | sha256sum -c - >/dev/null)
  skopeo inspect --raw "oci-archive:$work_dir/$archive" > "$work_dir/archive.json"
  python3 "$validator" verify "$work_dir/archive.json" "$digest" "$amd64" "$arm64"
  verify_labels "oci-archive:$work_dir/$archive"
  if remote_exists; then
    printf 'Beta image appeared with the expected digest: %s\n' "$image"
    continue
  else
    result=$?
    [[ "$result" == 1 ]] || exit "$result"
  fi
  skopeo copy --all --preserve-digests "oci-archive:$work_dir/$archive" "docker://$image"
  remote_exists || { printf 'ERROR: imported beta image verification failed\n' >&2; exit 1; }
  verify_labels "docker://$image"
  printf 'Imported and verified immutable beta image: %s\n' "$image"
  rm -f "$work_dir/$archive"
done < "$work_dir/plan.tsv"
