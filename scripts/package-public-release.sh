#!/usr/bin/env bash
# Beta bundles expose only the customer deployment surface of the public tag.
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="${1:?usage: package-public-release.sh VERSION OUTPUT}"
output="${2:?usage: package-public-release.sh VERSION OUTPUT}"
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-cpa\.[0-9]+(-beta\.[1-9][0-9]*)?$ ]] || {
  printf 'ERROR: invalid public release version\n' >&2
  exit 1
}
[[ "$output" == /* ]] || output="$PWD/$output"
mkdir -p "$(dirname "$output")"
cd "$repo_root"

if [[ "$version" != *-beta.* ]]; then
  git archive --format=tar.gz --prefix="CLIProxyAPI-CPA-${version}/" "$version" > "$output"
  exit 0
fi

files=(
  LICENSE README.md README_CN.md RELEASES.md RELEASES_CN.md RELEASE-CANDIDATE.md
  .env.example config.example.yaml docker-compose.yml management.html
  release-manifest.json release-catalog.json
  install-cpa-release.sh install-cpa-cli-release.sh install-cpamp-release.sh
  docs/deployment-cpa-cpamp.zh-CN.md docs/release-beta-20260911.zh-CN.md
  deploy/cpamp-pool-server/.env.example deploy/cpamp-pool-server/.gitignore
  deploy/cpamp-pool-server/README.md deploy/cpamp-pool-server/bootstrap.sh
  deploy/cpamp-pool-server/compose.yml deploy/cpamp-pool-server/config.yaml.template
  deploy/cpamp-pool-server/preflight.sh
  scripts/check-license-deployment.sh scripts/check-license-runtime.sh
)
for file in "${files[@]}"; do
  [[ "$(git cat-file -t "$version:$file" 2>/dev/null)" == blob ]] || {
    printf 'ERROR: public deployment asset is missing: %s\n' "$file" >&2
    exit 1
  }
done
git archive --format=tar.gz --prefix="CLIProxyAPI-CPA-${version}/" "$version" -- "${files[@]}" > "$output"
