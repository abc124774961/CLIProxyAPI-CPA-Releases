# CPA-Pro release process

Customer deployments use the public release repository
[`abc124774961/CLIProxyAPI-CPA-Releases`](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases)
and a tag matching `vMAJOR.MINOR.PATCH-cpa.N`. A customer host clones that
immutable tag, builds the checked-out source, and starts only the CPA service.

## Required release gates

1. `go test ./...` and `go build ./cmd/server` pass on the release commit.
2. `docker compose --env-file .env.example config` renders with a test public
   key and the license state volume/secret mount is present.
3. `scripts/check-license-deployment.sh` passes before the first start.
4. The storefront exposes `POST /api/storefront/licenses/grace` and accepts
   the configured client credentials. A 404 is a release blocker.
5. The release tag and commit are recorded in the release notes together with
   SHA256 checksums for any published archives.
6. `release-manifest.json`, `.env.example`, and `config.example.yaml` contain
   the same release-pinned license and plugin public keys. Customer secrets are
   never included.
7. The installer uses an atomic clone, a per-install lock, a free-space gate,
   storefront preflight, runtime license verification, and previous-image
   rollback for upgrades.

The public repository must not contain customer `.env`, private signing keys,
license leases, `data/license`, or plugin artifacts that are encrypted for a
specific customer.
