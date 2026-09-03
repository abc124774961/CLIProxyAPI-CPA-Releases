# CPA-Pro release process

Customer deployments use the public release repository
[`abc124774961/CLIProxyAPI-CPA-Releases`](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases)
and a tag matching `vMAJOR.MINOR.PATCH-cpa.N`. The matching CPAMP pool bundle is
published separately as a pinned GHCR image. See [`RELEASES.md`](RELEASES.md),
[`RELEASES_CN.md`](RELEASES_CN.md), and [`release-catalog.json`](release-catalog.json)
for the current pair, source references, image digests, and deployment links.
A customer host may clone the immutable CPA tag or pull the published image;
the CPAMP stack uses the matching Manager + Agent image from the catalog.

## Required release gates

1. `go test ./...` and `go build ./cmd/server` pass on the release commit.
2. `docker compose --env-file .env.example config` renders with a test public
   key and the license state volume/secret mount is present.
3. `scripts/check-license-deployment.sh` passes before the first start. When
   `--provider` is used, configure a documented non-mutating storefront
   preflight URL and complete JSON body; only HTTP 2xx is accepted.
4. The storefront exposes `POST /api/storefront/licenses/grace` and accepts
   the configured client credentials for the real first-install flow. If the
   optional `--provider` check is enabled, it uses a separate documented
   non-mutating preflight URL and complete JSON body; a 400/401/403/404/405 or
   5xx from that preflight is a release blocker. The live grace route must
   never be probed with an empty request because it can create a persisted
   lease.
5. The release tag and commit are recorded in the release notes together with
   SHA256 checksums for any published archives.
6. `release-manifest.json`, `.env.example`, and `config.example.yaml` contain
   the same release-pinned license and plugin public keys. Customer secrets are
   never included.
7. `license.grace-period` is only a bounded local network-failure fallback and
   is capped at six hours in the release binary; storefront-signed
   `grace_until`/`expiry_grace_until` values remain authoritative.
8. The installer uses an atomic clone, a per-install lock, a free-space gate,
   storefront preflight, runtime license verification, and previous-image
   rollback for upgrades.
9. The catalog records both `linux/amd64` and `linux/arm64` for CPA CLI and
   CPAMP, and the CPAMP image is checked for both `cpa-manager-plus` and
   `cpamp-agent` binaries.

The public repository must not contain customer `.env`, private signing keys,
license leases, `data/license`, or plugin artifacts that are encrypted for a
specific customer.
