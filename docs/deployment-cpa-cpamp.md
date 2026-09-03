# Unified CPA CLI + CPAMP deployment

This page is the short customer-server entry point for published builds. The source projects are maintained
separately in [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) and
[CPA-Manager-Pro](https://github.com/abc124774961/CPA-Manager-Pro). This repository records released CPA CLI/CPAMP
versions, image digests, and deployment references. Use the local
[CPAMP pool-server template](../deploy/cpamp-pool-server/README.md); the independent source repository carries
[full parameter details](https://github.com/abc124774961/CPA-Manager-Pro/tree/main/deploy/pool-server).

## Current bundle

| Component | Version/tag | Pinned image | Platforms |
| --- | --- | --- | --- |
| CPA CLI | `v7.2.148-cpa.3` | `ghcr.io/abc124774961/cli-proxy-api-cpa:v7.2.148-cpa.3` | `linux/amd64`, `linux/arm64` |
| CPAMP (Manager + Agent) | `v1.12.8-cpa.1` / `cpamp-pool-v1.12.8-cpa.1` | `ghcr.io/abc124774961/cpa-manager-plus:v1.12.8-cpa.1` | `linux/amd64`, `linux/arm64` |

Manifest digests, source commit, and historical tags are recorded in
[`release-catalog.json`](../release-catalog.json). Pin a release tag (or the catalog digest) and never use `latest`.

## Short install flow

1. Check out the CPA release tag and copy `config.example.yaml` and `.env.example` into the deployment directory.
2. Copy the local [CPAMP pool-server template](../deploy/cpamp-pool-server/) into a separate stack directory.
3. Set `CPA_IMAGE` and `CPAMP_IMAGE` to the exact values above. Put the storefront-issued client secret in a mode-600 file and keep `data/license` persistent.
4. Run every Compose operation with the explicit env file:

   ```bash
   ./preflight.sh
   docker compose --env-file .env -f compose.yml pull
   docker compose --env-file .env -f compose.yml up -d
   ```

5. Check CPA `/healthz`, CPAMP `/health`, and the manager/agent logs before exposing the panel.

For CPA-only deployments, use `scripts/check-license-deployment.sh` and
`./install-cpa-release.sh` from this repository. The storefront at `https://p.666ttt.net/api/storefront` signs the
activation and grace lease; the local fallback is capped at six hours.

## Upgrade and rollback

Change only to a verified CPA/CPAMP tag or digest, pull first, then recreate the affected services. Keep the CPA
license directory, configuration, Manager database, and backups; do not run `down -v`. If a health or authorization
check fails, restore the previous pair of tags/digests and rerun the checks.

## References

- Release index: [`RELEASES.md`](../RELEASES.md) / [`release-catalog.json`](../release-catalog.json)
- CPA CLI details: [`README.md`](../README.md)
- CPAMP release template: [`deploy/cpamp-pool-server`](../deploy/cpamp-pool-server/)
- CPAMP source and full template: [CPA-Manager-Pro/deploy/pool-server](https://github.com/abc124774961/CPA-Manager-Pro/tree/main/deploy/pool-server)
