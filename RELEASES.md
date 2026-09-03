# CPA release index

This repository is the **CPA release hub**. It records installable CPA CLI versions, matching CPAMP images, and
short deployment entry points. Feature source is maintained separately in
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) and
[CPA-Manager-Pro](https://github.com/abc124774961/CPA-Manager-Pro). Deploy a Git tag and GHCR manifest digest; do not
use `latest`.

Machine-readable metadata, including per-platform digests: [`release-catalog.json`](release-catalog.json).

## Current verified bundle

| Component | Release | Tag | Image | Manifest digest | Platforms |
| --- | --- | --- | --- | --- | --- |
| CPA CLI | `v7.2.148-cpa.3` | [`v7.2.148-cpa.3`](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases/tag/v7.2.148-cpa.3) | `ghcr.io/abc124774961/cli-proxy-api-cpa:v7.2.148-cpa.3` | `sha256:a7938c5a716127829bd3a7fe524b6fbdf8509f44d1a0c8304983bff642cd8bad` | `linux/amd64`, `linux/arm64` |
| CPAMP (Manager + Agent) | `v1.12.8-cpa.1` | `cpamp-pool-v1.12.8-cpa.1` ([template](deploy/cpamp-pool-server/)) | `ghcr.io/abc124774961/cpa-manager-plus:v1.12.8-cpa.1` | `sha256:f1cde753aa222b77a11e387fbf73eeac106a0d78db236fd5f13bf31a0860352b` | `linux/amd64`, `linux/arm64` |

The CPAMP image includes both `cpa-manager-plus` and `cpamp-agent`; run both from the same image tag.

## CPA CLI history

| Tag | Status | Notes |
| --- | --- | --- |
| [`v7.2.148-cpa.3`](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/tree/v7.2.148-cpa.3) | Current | Recommended install; digest above |
| [`v7.2.148-cpa.2`](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/tree/v7.2.148-cpa.2) | Previous | Keep the pinned image digest when retained |
| [`v7.2.148-cpa.1`](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/tree/v7.2.148-cpa.1) | Tag only | Confirm image availability before use |

## Checksums and provenance

- CPA CLI release assets include `checksums.txt`, the manifest, and the version archive on the
  [v7.2.148-cpa.3 release page](https://github.com/abc124774961/CLIProxyAPI-CPA-Releases/releases/tag/v7.2.148-cpa.3).
- For containers, verify the GHCR manifest digest; use `docker buildx imagetools inspect <image>` for per-platform digests.
- CPAMP source, dual-architecture, and Manager/Agent checks are recorded in the
  [pool-image workflow](https://github.com/abc124774961/CPA-Manager-Pro/actions/runs/33803882344).

## Deployment entry points

- [Unified CPA CLI + CPAMP deployment](docs/deployment-cpa-cpamp.md)
- [CPA CLI installation](README.md#customer-server-deployment-docker-compose)
- [CPAMP release template](deploy/cpamp-pool-server/README.md)
- [CPAMP source/full template](https://github.com/abc124774961/CPA-Manager-Pro/tree/main/deploy/pool-server)
- [Release manifest](release-manifest.json)

## Release rules

1. CPA CLI tags use `vMAJOR.MINOR.PATCH-cpa.N`; CPAMP pool bundles use a `cpamp-pool-vX.Y.Z-cpa.N` source tag and a
   matching `vX.Y.Z-cpa.N` image tag.
2. Every release records the source commit, image manifest digest, both Linux platforms, and reproducible checksums.
3. Run `scripts/verify-release-bundle.sh <CPA_TAG>` before publishing. Never commit customer `.env`, secrets, license
   leases, or customer-encrypted plugin packages.
4. The storefront is `https://p.666ttt.net/api/storefront`; signed activation and expiry-grace leases are authoritative,
   with a six-hour maximum local fallback.
