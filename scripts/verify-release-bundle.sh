#!/usr/bin/env bash

# Verify that a CPA public-release checkout is complete, internally consistent,
# renderable, and free of customer-specific secrets/state before tagging it.

set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
expected_version="${1:-}"
cd "$repo_root"

for command in git python3 docker; do
  command -v "$command" >/dev/null 2>&1 || { echo "FAIL: $command is required" >&2; exit 1; }
done
docker compose version >/dev/null 2>&1 || { echo "FAIL: Docker Compose v2 is required" >&2; exit 1; }

required_files=(
  release-manifest.json
  release-catalog.json
  management.html
  install-cpa-release.sh
  install-cpa-cli-release.sh
  install-cpamp-release.sh
  docker-compose.yml
  .env.example
  config.example.yaml
  README.md
  README_CN.md
  RELEASES.md
  RELEASES_CN.md
  RELEASE-CANDIDATE.md
  docs/deployment-cpa-cpamp.zh-CN.md
  deploy/cpamp-pool-server/.env.example
  deploy/cpamp-pool-server/.gitignore
  deploy/cpamp-pool-server/README.md
  deploy/cpamp-pool-server/bootstrap.sh
  deploy/cpamp-pool-server/compose.yml
  deploy/cpamp-pool-server/config.yaml.template
  deploy/cpamp-pool-server/preflight.sh
  scripts/check-license-deployment.sh
  scripts/check-license-deployment_test.sh
  scripts/check-license-runtime.sh
  scripts/check-license-runtime_test.sh
  scripts/package-cpa-release.sh
  scripts/package-cpamp-release.sh
)
for path in "${required_files[@]}"; do
  [[ -f "$path" ]] || { echo "FAIL: release file is missing: $path" >&2; exit 1; }
done
for path in install-cpa-release.sh install-cpa-cli-release.sh install-cpamp-release.sh scripts/package-cpa-release.sh scripts/package-cpamp-release.sh scripts/check-license-deployment.sh scripts/check-license-runtime.sh scripts/check-license-runtime_test.sh scripts/verify-release-bundle.sh deploy/cpamp-pool-server/bootstrap.sh deploy/cpamp-pool-server/preflight.sh; do
  [[ -x "$path" ]] || { echo "FAIL: release script is not executable: $path" >&2; exit 1; }
done
bash -n install-cpa-release.sh install-cpa-cli-release.sh install-cpamp-release.sh scripts/package-cpa-release.sh scripts/package-cpamp-release.sh scripts/check-license-deployment.sh scripts/check-license-runtime.sh scripts/check-license-runtime_test.sh scripts/verify-release-bundle.sh deploy/cpamp-pool-server/bootstrap.sh deploy/cpamp-pool-server/preflight.sh

for ignore_entry in '.env' 'data' 'secrets' 'plugins' '*.key'; do
  grep -Fx -- "$ignore_entry" .dockerignore >/dev/null 2>&1 || {
    echo "FAIL: .dockerignore must exclude deployment material: $ignore_entry" >&2
    exit 1
  }
done
for ignore_entry in '.env' 'data/' 'backups/' 'secrets/*'; do
  grep -Fx -- "$ignore_entry" deploy/cpamp-pool-server/.gitignore >/dev/null 2>&1 || {
    echo "FAIL: CPAMP template .gitignore must exclude deployment material: $ignore_entry" >&2
    exit 1
  }
done

# Google OAuth client secrets must be supplied by the deployment environment;
# a public release must never carry a hard-coded GOCSPX credential.
if oauth_secret_hits="$(git grep -nE 'GOCS[P]X-[A-Za-z0-9_-]+' -- ':!scripts/verify-release-bundle.sh')"; then
  echo "FAIL: public release contains a hard-coded Google OAuth client secret:" >&2
  printf '%s\n' "$oauth_secret_hits" >&2
  exit 1
fi

python3 - "$expected_version" <<'PY'
import base64
import hashlib
import json
import os
import re
import sys
import tarfile
from pathlib import Path

expected = sys.argv[1].strip()
manifest = json.loads(Path("release-manifest.json").read_text(encoding="utf-8"))
version = str(manifest.get("version", ""))
if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+-cpa\.[0-9]+", version):
    raise SystemExit("FAIL: release-manifest.json contains an invalid version")
if expected and version != expected:
    raise SystemExit(f"FAIL: manifest version {version} does not match {expected}")
if manifest.get("repository") != "abc124774961/CLIProxyAPI-CPA-Releases":
    raise SystemExit("FAIL: release repository metadata is inconsistent")

license_meta = manifest.get("license") or {}
for field, fingerprint_field in (
    ("public_key", "public_key_sha256"),
    ("plugin_public_key", "plugin_public_key_sha256"),
):
    value = str(license_meta.get(field, ""))
    padded = value + "=" * ((4 - len(value) % 4) % 4)
    try:
        raw = base64.urlsafe_b64decode(padded)
    except Exception as exc:
        raise SystemExit(f"FAIL: {field} is invalid: {exc}")
    if len(raw) != 32:
        raise SystemExit(f"FAIL: {field} must decode to 32 bytes")
    if hashlib.sha256(raw).hexdigest() != license_meta.get(fingerprint_field):
        raise SystemExit(f"FAIL: {field} fingerprint mismatch")

if license_meta.get("api_base_url") != "https://p.666ttt.net/api/storefront":
    raise SystemExit("FAIL: storefront API base URL is inconsistent")
if license_meta.get("grace_authority") != "storefront" or license_meta.get("default_grace_period") != "6h":
    raise SystemExit("FAIL: grace policy metadata is inconsistent")

# The release catalog is the public index for both components. Keep its
# current records aligned with the signed manifest so a deployment cannot
# silently combine a CPA image with a different CPAMP image or tag.
catalog = json.loads(Path("release-catalog.json").read_text(encoding="utf-8"))
if catalog.get("schema_version") != 1:
    raise SystemExit("FAIL: release-catalog.json contains an unsupported schema version")
if catalog.get("repository") != manifest.get("repository"):
    raise SystemExit("FAIL: release catalog repository metadata is inconsistent")
if catalog.get("manifest") != "release-manifest.json":
    raise SystemExit("FAIL: release catalog must point to release-manifest.json")

manifest_components = manifest.get("components") or {}
catalog_components = catalog.get("components") or {}
for component_name in ("cpa_cli", "cpamp"):
    manifest_component = manifest_components.get(component_name) or {}
    catalog_component = catalog_components.get(component_name) or {}
    current = catalog_component.get("current") or {}
    if not manifest_component or not catalog_component or not current:
        raise SystemExit(f"FAIL: release metadata is missing component {component_name}")
    for field in ("version", "tag", "image", "image_digest", "platforms", "platform_digests"):
        if current.get(field) != manifest_component.get(field):
            raise SystemExit(f"FAIL: {component_name} catalog {field} does not match release-manifest.json")
    if component_name == "cpamp":
        for field in ("included_binaries", "artifact_manifest", "artifact_checksums", "panel_asset", "panel_asset_sha256", "artifacts"):
            if current.get(field) != manifest_component.get(field):
                raise SystemExit(f"FAIL: CPAMP catalog {field} does not match release-manifest.json")
    else:
        for field in ("artifact_manifest", "artifact_checksums", "artifacts", "binary", "binary_install_script", "image_archive", "image_prebuilt"):
            if current.get(field) != manifest_component.get(field):
                raise SystemExit(f"FAIL: CPA CLI catalog {field} does not match release-manifest.json")
    for field in ("source_repository", "release_url"):
        value = str(manifest_component.get(field, ""))
        if not value.startswith("https://github.com/"):
            raise SystemExit(f"FAIL: {component_name} {field} must be an HTTPS GitHub URL")
        if component_name == "cpa_cli" and field == "release_url" and expected:
            expected_release_url = f"https://github.com/{manifest.get('repository', '')}/releases/tag/{expected}"
            if value != expected_release_url:
                raise SystemExit("FAIL: CPA CLI release_url does not match the requested release tag")
    if component_name == "cpamp":
        release_repository = str(manifest_component.get("release_repository", ""))
        if release_repository != "https://github.com/abc124774961/CLIProxyAPI-CPA-Releases":
            raise SystemExit("FAIL: CPAMP release_repository must point to the public release repository")
        if catalog_component.get("release_repository") != release_repository:
            raise SystemExit("FAIL: CPAMP catalog release_repository does not match release-manifest.json")
        if expected:
            expected_release_url = f"{release_repository}/releases/tag/{expected}"
            if str(manifest_component.get("release_url", "")) != expected_release_url:
                raise SystemExit("FAIL: CPAMP release_url does not match the requested CPA release tag")
    digest = str(current.get("image_digest", ""))
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
        raise SystemExit(f"FAIL: {component_name} image_digest must be a sha256 digest")
    platforms = current.get("platforms")
    if sorted(platforms or []) != ["linux/amd64", "linux/arm64"]:
        raise SystemExit(f"FAIL: {component_name} must publish linux/amd64 and linux/arm64")
    platform_digests = current.get("platform_digests") or {}
    if set(platform_digests) != {"linux/amd64", "linux/arm64"}:
        raise SystemExit(f"FAIL: {component_name} must record both platform digests")
    for platform, platform_digest in platform_digests.items():
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", str(platform_digest)):
            raise SystemExit(f"FAIL: {component_name} {platform} digest is invalid")
    image = str(current.get("image", ""))
    if not image or image.endswith(":latest") or "@sha256:" in image:
        raise SystemExit(f"FAIL: {component_name} image must use an immutable version tag")
    if component_name == "cpa_cli":
        if not str(current.get("checksums_url", "")).endswith("/checksums.txt"):
            raise SystemExit("FAIL: CPA CLI catalog entry must link to checksums.txt")
    else:
        if sorted(current.get("included_binaries") or []) != ["cpa-manager-plus", "cpamp-agent"]:
            raise SystemExit("FAIL: CPAMP catalog entry must include Manager and Agent binaries")
        component_version = str(manifest_component.get("version", ""))
        artifact_manifest = str(current.get("artifact_manifest", ""))
        expected_manifest = f"CPAMP-{component_version}.manifest.json"
        if artifact_manifest != expected_manifest:
            raise SystemExit(f"FAIL: CPAMP artifact_manifest must be {expected_manifest}")
        if current.get("artifact_checksums") != "checksums.txt":
            raise SystemExit("FAIL: CPAMP artifact_checksums must point to the release checksums.txt")
        panel_asset = str(current.get("panel_asset", ""))
        panel_asset_sha256 = str(current.get("panel_asset_sha256", ""))
        if panel_asset != "management.html" or not Path(panel_asset).is_file():
            raise SystemExit("FAIL: CPAMP panel_asset must be the checked-in management.html")
        if not re.fullmatch(r"[0-9a-f]{64}", panel_asset_sha256):
            raise SystemExit("FAIL: CPAMP panel_asset_sha256 is invalid")
        if hashlib.sha256(Path(panel_asset).read_bytes()).hexdigest() != panel_asset_sha256:
            raise SystemExit("FAIL: management.html checksum does not match release metadata")
        artifacts = current.get("artifacts")
        expected_artifacts = {
            "linux/amd64": f"CPAMP-{component_version}-linux-amd64.tar.gz",
            "linux/arm64": f"CPAMP-{component_version}-linux-arm64.tar.gz",
        }
        if artifacts != expected_artifacts:
            raise SystemExit("FAIL: CPAMP artifacts must provide the linux/amd64 and linux/arm64 bundles")
        release_url = str(current.get("release_url", ""))
        public_release_prefix = f"https://github.com/{manifest.get('repository', '')}/releases/tag/"
        if not release_url.startswith(public_release_prefix):
            raise SystemExit("FAIL: CPAMP release_url must point to a public release in this repository")
        if expected and release_url != f"{public_release_prefix}{expected}":
            raise SystemExit("FAIL: CPAMP catalog release_url does not match the requested CPA release tag")

    published_tags = catalog_component.get("published_tags")
    if not isinstance(published_tags, list) or not published_tags:
        raise SystemExit(f"FAIL: {component_name} catalog has no published tags")
    for entry in published_tags:
        if not isinstance(entry, dict) or not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+-cpa\.[0-9]+", str(entry.get("version", ""))):
            raise SystemExit(f"FAIL: {component_name} catalog contains an invalid published tag")
        tag_url = str(entry.get("tag_url", ""))
        if not tag_url.startswith("https://github.com/"):
            raise SystemExit(f"FAIL: {component_name} published tag URL is invalid")
        if component_name == "cpamp" and not tag_url.startswith(
            f"https://github.com/{manifest.get('repository', '')}/releases/"
        ):
            raise SystemExit("FAIL: CPAMP published tag URL must point to the public release repository")

deployment = catalog.get("deployment") or {}
if deployment.get("customer_artifacts_only") is not True or deployment.get("private_source_access_required") is not False:
    raise SystemExit("FAIL: deployment metadata must declare public artifacts-only customer flow")
for field in ("guide_zh_cn",):
    path = str(deployment.get(field, ""))
    if not path or not Path(path).is_file():
        raise SystemExit(f"FAIL: release catalog deployment guide is missing: {path}")
references = deployment.get("references") or {}
for name, url in references.items():
    if not isinstance(url, str) or not url.startswith("https://"):
        raise SystemExit(f"FAIL: deployment reference {name} must be an HTTPS URL")
manifest_deployment = manifest.get("deployment") or {}
if manifest_deployment.get("customer_artifacts_only") is not True or manifest_deployment.get("private_source_access_required") is not False:
    raise SystemExit("FAIL: manifest deployment metadata must declare public artifacts-only customer flow")
manifest_references = manifest_deployment.get("references") or {}
for name, url in manifest_references.items():
    if not isinstance(url, str) or not url.startswith("https://"):
        raise SystemExit(f"FAIL: manifest deployment reference {name} must be an HTTPS URL")
for field in ("template_dir", "compose_file", "readme"):
    catalog_path = str(deployment.get(field, ""))
    manifest_path = str(manifest_deployment.get(field, ""))
    if not catalog_path or catalog_path != manifest_path or not Path(catalog_path).exists():
        raise SystemExit(f"FAIL: deployment {field} does not resolve to the checked-in CPAMP template")
for path in (
    "deploy/cpamp-pool-server/.env.example",
    "deploy/cpamp-pool-server/.gitignore",
    "deploy/cpamp-pool-server/README.md",
    "deploy/cpamp-pool-server/bootstrap.sh",
    "deploy/cpamp-pool-server/compose.yml",
    "deploy/cpamp-pool-server/config.yaml.template",
    "deploy/cpamp-pool-server/preflight.sh",
):
    if not Path(path).is_file():
        raise SystemExit(f"FAIL: CPAMP template file is missing: {path}")

def parse_dotenv(path):
    values = {}
    for raw_line in Path(path).read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values.setdefault(key.strip(), value.strip().strip('"\''))
    return values

pool_env_values = parse_dotenv("deploy/cpamp-pool-server/.env.example")
for env_name, component_name in (
    ("CPA_IMAGE", "cpa_cli"),
    ("CPAMP_IMAGE", "cpamp"),
):
    if pool_env_values.get(env_name) != manifest_components[component_name].get("image"):
        raise SystemExit(f"FAIL: CPAMP template {env_name} does not match release-manifest.json")
for env_name, field in (
    ("CPA_LICENSE_PUBLIC_KEY", "public_key"),
    ("CPA_LICENSE_PLUGIN_PUBLIC_KEY", "plugin_public_key"),
):
    if pool_env_values.get(env_name) != license_meta.get(field):
        raise SystemExit(f"FAIL: CPAMP template {env_name} does not match release-manifest.json")
if pool_env_values.get("CPA_LICENSE_CLIENT_SECRET", ""):
    raise SystemExit("FAIL: CPAMP template contains a storefront client secret")
pool_config = Path("deploy/cpamp-pool-server/config.yaml.template").read_text(encoding="utf-8")
panel_repo = re.search(r'(?m)^\s*panel-github-repository:\s*"https://github.com/([^/\"]+/[^\"]+)"', pool_config)
if not panel_repo or panel_repo.group(1) != "abc124774961/CLIProxyAPI-CPA-Releases":
    raise SystemExit("FAIL: CPAMP template panel repository must point to the public release repository")
if re.search(r"(?m)^\s*client-secret:\s*[^\s\"']", pool_config):
    raise SystemExit("FAIL: CPAMP config template contains a client secret")
pool_compose = Path("deploy/cpamp-pool-server/compose.yml").read_text(encoding="utf-8")
if re.search(r"(?m)^\s*image:\s*local/", pool_compose):
    raise SystemExit("FAIL: CPAMP compose template uses a local image")

env_values = {}
for raw_line in Path(".env.example").read_text(encoding="utf-8").splitlines():
    line = raw_line.strip()
    if not line or line.startswith("#") or "=" not in line:
        continue
    key, value = line.split("=", 1)
    env_values.setdefault(key.strip(), value.strip().strip('"\''))
for env_name, field in (
    ("CPA_LICENSE_PUBLIC_KEY", "public_key"),
    ("CPA_LICENSE_PLUGIN_PUBLIC_KEY", "plugin_public_key"),
):
    if env_values.get(env_name) != license_meta.get(field):
        raise SystemExit(f"FAIL: {env_name} does not match release-manifest.json")
if env_values.get("CPA_LICENSE_CLIENT_SECRET"):
    raise SystemExit("FAIL: .env.example contains a customer client secret")
compose_text = Path("docker-compose.yml").read_text(encoding="utf-8")
expected_cpa_image = manifest_components["cpa_cli"].get("image")
if f"image: ${{CLI_PROXY_IMAGE:-{expected_cpa_image}}}" not in compose_text:
    raise SystemExit("FAIL: docker-compose.yml default CPA image does not match release-manifest.json")
if "eceasy/cli-proxy-api:latest" in compose_text:
    raise SystemExit("FAIL: docker-compose.yml contains the mutable upstream latest image")

# Parse the small, stable license section in config.example.yaml without
# requiring PyYAML. Public releases must keep both template sources aligned so
# a customer who chooses YAML instead of .env receives the same publisher keys.
config_values = {}
in_license = False
for raw_line in Path("config.example.yaml").read_text(encoding="utf-8").splitlines():
    if re.match(r"^license:\s*(?:#.*)?$", raw_line):
        in_license = True
        continue
    if in_license and raw_line and not raw_line[0].isspace():
        in_license = False
    if not in_license:
        continue
    match = re.match(r"^\s*(public-key|plugin-public-key|client-id|client-secret):\s*(.*?)\s*$", raw_line)
    if not match:
        continue
    value = re.sub(r"\s+#.*$", "", match.group(2)).strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        value = value[1:-1]
    config_values[match.group(1)] = value
for config_name, manifest_name in (
    ("public-key", "public_key"),
    ("plugin-public-key", "plugin_public_key"),
):
    if config_values.get(config_name) != license_meta.get(manifest_name):
        raise SystemExit(f"FAIL: config.example.yaml {config_name} does not match release-manifest.json")
for secret_name in ("client-id", "client-secret"):
    if config_values.get(secret_name, ""):
        raise SystemExit(f"FAIL: config.example.yaml contains a customer {secret_name}")

# When the package job has already assembled release assets, set
# CPA_RELEASE_ARTIFACT_DIR to verify the actual archives as well as the source
# metadata.  The source-only invocation leaves this unset so local checks do
# not require a release build or a large image pull.
artifact_dir_value = os.environ.get("CPA_RELEASE_ARTIFACT_DIR", "").strip()
if artifact_dir_value:
    artifact_dir = Path(artifact_dir_value)
    if not artifact_dir.is_dir():
        raise SystemExit(f"FAIL: release artifact directory is missing: {artifact_dir}")

    # Validate both component manifests before looking at the tarballs. A
    # release is useful to ordinary users only when the CPA CLI and CPAMP
    # platform assets are present together and point at the same pinned images.
    def read_json_file(path, label):
        try:
            return json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise SystemExit(f"FAIL: {label} is not valid JSON: {exc}")

    def read_json_file_from_bytes(data, label):
        try:
            return json.loads(data.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise SystemExit(f"FAIL: {label} is not valid JSON: {exc}")

    cpa_manifest_component = manifest_components.get("cpa_cli") or {}
    cpamp_manifest_component = manifest_components.get("cpamp") or {}
    cpa_manifest_name = str(cpa_manifest_component.get("artifact_manifest", ""))
    cpamp_manifest_name = str(cpamp_manifest_component.get("artifact_manifest", ""))
    if not cpa_manifest_name:
        raise SystemExit("FAIL: release-manifest.json does not name a CPA CLI artifact manifest")
    if not cpamp_manifest_name:
        raise SystemExit("FAIL: release-manifest.json does not name a CPAMP artifact manifest")
    cpa_manifest_path = artifact_dir / cpa_manifest_name
    cpamp_manifest_path = artifact_dir / cpamp_manifest_name
    if not cpa_manifest_path.is_file():
        raise SystemExit(f"FAIL: CPA CLI artifact manifest is missing: {cpa_manifest_path}")
    if not cpamp_manifest_path.is_file():
        raise SystemExit(f"FAIL: CPAMP artifact manifest is missing: {cpamp_manifest_path}")
    packaged_cpa_manifest = read_json_file(cpa_manifest_path, "CPA CLI artifact manifest")
    packaged_cpamp_manifest = read_json_file(cpamp_manifest_path, "CPAMP artifact manifest")

    expected_platforms = ["linux/amd64", "linux/arm64"]
    expected_cpa_artifacts = {
        platform: f"CPA-CLI-{cpa_manifest_component.get('version')}-linux-{platform.split('/', 1)[1]}.tar.gz"
        for platform in expected_platforms
    }
    expected_cpamp_artifacts = {
        platform: f"CPAMP-{cpamp_manifest_component.get('version')}-linux-{platform.split('/', 1)[1]}.tar.gz"
        for platform in expected_platforms
    }
    if packaged_cpa_manifest.get("schema_version") != 1 or packaged_cpa_manifest.get("component") != "cpa_cli":
        raise SystemExit("FAIL: CPA CLI artifact manifest schema/component is invalid")
    for field in ("version", "image", "image_digest", "source_commit"):
        if packaged_cpa_manifest.get(field) != cpa_manifest_component.get(field):
            raise SystemExit(f"FAIL: CPA CLI artifact manifest {field} does not match release-manifest.json")
    if packaged_cpa_manifest.get("image_load_ref") != cpa_manifest_component.get("image"):
        raise SystemExit("FAIL: CPA CLI artifact manifest image_load_ref does not match image")
    if packaged_cpa_manifest.get("platforms") != expected_platforms:
        raise SystemExit("FAIL: CPA CLI artifact manifest must list both supported platforms")
    if packaged_cpa_manifest.get("artifacts") != expected_cpa_artifacts:
        raise SystemExit("FAIL: CPA CLI artifact manifest artifact names do not match release metadata")
    if packaged_cpa_manifest.get("platform_digests") != cpa_manifest_component.get("platform_digests"):
        raise SystemExit("FAIL: CPA CLI artifact manifest platform digests do not match release-manifest.json")
    if packaged_cpamp_manifest.get("schema_version") != 1 or packaged_cpamp_manifest.get("component") != "cpamp":
        raise SystemExit("FAIL: CPAMP artifact manifest schema/component is invalid")
    for field in ("version", "image", "image_digest", "source_commit", "panel_asset", "panel_asset_sha256"):
        if packaged_cpamp_manifest.get(field) != cpamp_manifest_component.get(field):
            raise SystemExit(f"FAIL: CPAMP artifact manifest {field} does not match release-manifest.json")
    if packaged_cpamp_manifest.get("platforms") != expected_platforms:
        raise SystemExit("FAIL: CPAMP artifact manifest must list both supported platforms")
    if packaged_cpamp_manifest.get("artifacts") != expected_cpamp_artifacts:
        raise SystemExit("FAIL: CPAMP artifact manifest artifact names do not match release metadata")
    if packaged_cpamp_manifest.get("platform_digests") != cpamp_manifest_component.get("platform_digests"):
        raise SystemExit("FAIL: CPAMP artifact manifest platform digests do not match release-manifest.json")

    # A component manifest can describe a developer-selected online-only bundle
    # (image archive omitted). The public release workflow uses the default
    # self-contained mode, while the installer also accepts the explicit
    # online-only form. Keep the path and tag fields internally consistent.
    cpa_base_contents = [
        "bin/CLIProxyAPI",
        "config/config.example.yaml",
        "config/.env.example",
        "run.sh",
        "scripts/check-license-runtime.sh",
        "LICENSE",
    ]
    cpamp_base_contents = [
        "bin/cpa-manager-plus",
        "bin/cpamp-agent",
        "management.html",
        "deploy/cpamp-pool-server",
        "docs/deployment-cpa-cpamp.zh-CN.md",
    ]
    cpa_contents = packaged_cpa_manifest.get("contents")
    cpamp_contents = packaged_cpamp_manifest.get("contents")
    if cpa_contents not in (cpa_base_contents, cpa_base_contents + ["image/cli-proxy-api-cpa.tar"]):
        raise SystemExit("FAIL: CPA CLI artifact manifest contents are incomplete or contain an invalid path")
    if cpamp_contents not in (cpamp_base_contents, cpamp_base_contents + ["image/cpamp-image.tar"]):
        raise SystemExit("FAIL: CPAMP artifact manifest contents are incomplete or contain an invalid path")
    cpa_has_image = "image/cli-proxy-api-cpa.tar" in cpa_contents
    cpamp_has_image = "image/cpamp-image.tar" in cpamp_contents
    if packaged_cpa_manifest.get("image_archive_tag") != (cpa_manifest_component.get("image") if cpa_has_image else None):
        raise SystemExit("FAIL: CPA CLI artifact manifest image_archive_tag does not match its contents")
    if packaged_cpamp_manifest.get("image_archive_tag") != (cpamp_manifest_component.get("image") if cpamp_has_image else None):
        raise SystemExit("FAIL: CPAMP artifact manifest image_archive_tag does not match its contents")

    panel_asset_name = str(cpamp_manifest_component.get("panel_asset", ""))
    panel_asset_sha256 = str(cpamp_manifest_component.get("panel_asset_sha256", ""))
    published_panel = artifact_dir / panel_asset_name
    if panel_asset_name != "management.html" or not published_panel.is_file():
        raise SystemExit("FAIL: release assets are missing management.html")
    if hashlib.sha256(published_panel.read_bytes()).hexdigest() != panel_asset_sha256:
        raise SystemExit("FAIL: published management.html digest does not match release metadata")

    checksums_path = artifact_dir / "checksums.txt"
    if not checksums_path.is_file():
        raise SystemExit("FAIL: release artifact checksums.txt is missing")
    checksum_records = {}
    for raw_line in checksums_path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line:
            continue
        match = re.fullmatch(r"([0-9a-fA-F]{64})\s+[* ]?(.+)", line)
        if not match:
            raise SystemExit(f"FAIL: malformed checksum record: {raw_line}")
        asset_name = match.group(2)
        if asset_name in checksum_records:
            raise SystemExit(f"FAIL: checksums.txt contains a duplicate asset: {asset_name}")
        checksum_records[asset_name] = match.group(1).lower()

    expected_component_files = (
        set(expected_cpa_artifacts.values())
        | {cpa_manifest_name}
        | set(expected_cpamp_artifacts.values())
        | {cpamp_manifest_name}
    )
    for name in expected_component_files:
        path = artifact_dir / name
        if not path.is_file() or path.stat().st_size == 0:
            raise SystemExit(f"FAIL: release asset is missing or empty: {path}")
        if checksum_records.get(name) is None:
            raise SystemExit(f"FAIL: checksums.txt does not cover release asset: {name}")
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        if digest != checksum_records[name]:
            raise SystemExit(f"FAIL: checksum mismatch for release asset: {name}")

    # Every published file (apart from checksums.txt itself) must be covered by
    # the aggregate checksum list. This prevents a newly added binary or
    # metadata file from bypassing integrity verification.
    published_files = {p.name for p in artifact_dir.iterdir() if p.is_file() and p.name != "checksums.txt"}
    if published_files != set(checksum_records):
        missing = sorted(published_files - set(checksum_records))
        extra = sorted(set(checksum_records) - published_files)
        details = []
        if missing:
            details.append("missing=" + ",".join(missing))
        if extra:
            details.append("unknown=" + ",".join(extra))
        raise SystemExit("FAIL: checksums.txt does not exactly cover release assets (" + "; ".join(details) + ")")

    forbidden_fragments = (
        "/.env",
        "/secrets/",
        "/data/",
        "/data/license/",
        "/internal/api/data/",
        "/backups/",
        "/logs/",
        "/auths/",
        "/plugins/",
    )

    def inspect_archive(archive_path, label):
        try:
            archive = tarfile.open(archive_path, mode="r:gz")
        except tarfile.TarError as exc:
            raise SystemExit(f"FAIL: release asset is not a readable tar.gz ({label}): {exc}")
        try:
            members = archive.getmembers()
            if not members:
                raise SystemExit(f"FAIL: release archive is empty: {label}")
            roots = {Path(m.name.replace("\\", "/")).parts[0] for m in members if m.name}
            if len(roots) != 1:
                raise SystemExit(f"FAIL: release archive must have one top-level directory: {label}")
            root = next(iter(roots))
            relative_members = {}
            for member in members:
                member_name = member.name.replace("\\", "/")
                parts = Path(member_name).parts
                if member_name.startswith("/") or ".." in parts:
                    raise SystemExit(f"FAIL: release archive contains an unsafe path: {label}:{member_name}")
                relative = "/".join(parts[1:]) if len(parts) > 1 else ""
                if relative:
                    if relative in relative_members:
                        raise SystemExit(f"FAIL: release archive contains duplicate paths: {label}:{relative}")
                    relative_members[relative] = member
                    if any(fragment in ("/" + relative) for fragment in forbidden_fragments):
                        # `.env.example` is a public template; only reject the
                        # actual `.env` file and runtime/secret directories.
                        if not relative.endswith(".env.example"):
                            raise SystemExit(f"FAIL: release archive contains customer runtime material: {label}:{relative}")
            return archive, members, relative_members, root
        except BaseException:
            archive.close()
            raise

    def read_member_bytes(archive, member, label):
        handle = archive.extractfile(member)
        if handle is None:
            raise SystemExit(f"FAIL: release archive member is not readable ({label}): {member.name}")
        try:
            return handle.read()
        finally:
            handle.close()

    def inspect_image_archive(outer_archive, image_member, expected_image, label):
        if image_member is None:
            raise SystemExit(f"FAIL: {label} has no image archive")
        image_stream = outer_archive.extractfile(image_member)
        if image_stream is None:
            raise SystemExit(f"FAIL: {label} image archive member is not readable")
        try:
            with image_stream, tarfile.open(fileobj=image_stream, mode="r:") as image_archive:
                manifest_member = image_archive.getmember("manifest.json")
                image_manifest = json.loads(image_archive.extractfile(manifest_member).read().decode("utf-8"))
        except (KeyError, OSError, tarfile.TarError, json.JSONDecodeError, AttributeError) as exc:
            raise SystemExit(f"FAIL: image archive is unreadable ({label}): {exc}")
        if not isinstance(image_manifest, list) or len(image_manifest) != 1:
            raise SystemExit(f"FAIL: image archive must contain one platform image ({label})")
        tags = image_manifest[0].get("RepoTags") or []
        if tags != [expected_image]:
            raise SystemExit(
                f"FAIL: image archive tag mismatch ({label}): "
                f"expected {[expected_image]!r}, got {tags!r}"
            )

    # CPA CLI bundles: verify the binary, templates, per-platform digest, and
    # optional offline image archive. This is the artifact ordinary users run
    # on a customer server; checking only the source archive would miss a
    # broken or cross-architecture binary.
    cpa_required_files = {
        "package-manifest.json",
        "bin/CLIProxyAPI",
        "config/config.example.yaml",
        "config/.env.example",
        "run.sh",
        "scripts/check-license-runtime.sh",
        "LICENSE",
        "README.zh-CN.md",
    }
    for platform, name in expected_cpa_artifacts.items():
        archive, members, relative_members, root = inspect_archive(artifact_dir / name, name)
        expected_root = f"CPA-CLI-{cpa_manifest_component.get('version')}-{platform.replace('/', '-')}"
        if root != expected_root:
            archive.close()
            raise SystemExit(f"FAIL: CPA CLI archive top-level directory mismatch ({name}): {root}")
        required = set(cpa_required_files)
        package_manifest = read_json_file_from_bytes(
            read_member_bytes(archive, relative_members.get("package-manifest.json"), name),
            f"CPA CLI package manifest ({name})",
        ) if relative_members.get("package-manifest.json") else None
        if package_manifest is None:
            raise SystemExit(f"FAIL: CPA CLI archive has no package-manifest.json: {name}")
        package_has_image = package_manifest.get("image_archive") is not None
        if package_has_image:
            required.add("image/cli-proxy-api-cpa.tar")
        if set(cpa_required_files) - set(relative_members):
            missing = sorted(set(cpa_required_files) - set(relative_members))
            raise SystemExit(f"FAIL: CPA CLI archive is missing package files ({name}): {', '.join(missing)}")
        if package_has_image and "image/cli-proxy-api-cpa.tar" not in relative_members:
            raise SystemExit(f"FAIL: CPA CLI archive is missing image archive: {name}")
        if not package_has_image and "image/cli-proxy-api-cpa.tar" in relative_members:
            raise SystemExit(f"FAIL: CPA CLI package manifest omits an image archive that is present: {name}")
        if package_manifest.get("schema_version") != 1 or package_manifest.get("component") != "cpa_cli":
            raise SystemExit(f"FAIL: CPA CLI package manifest schema/component mismatch: {name}")
        for field in ("version", "image", "image_digest", "source_commit"):
            if package_manifest.get(field) != cpa_manifest_component.get(field):
                raise SystemExit(f"FAIL: CPA CLI package manifest {field} mismatch: {name}")
        if package_manifest.get("platform") != platform:
            raise SystemExit(f"FAIL: CPA CLI package manifest platform mismatch: {name}")
        expected_platform_digest = (cpa_manifest_component.get("platform_digests") or {}).get(platform)
        if package_manifest.get("platform_digest") != expected_platform_digest:
            raise SystemExit(f"FAIL: CPA CLI package manifest platform_digest mismatch: {name}")
        if package_manifest.get("binary") != "bin/CLIProxyAPI":
            raise SystemExit(f"FAIL: CPA CLI package manifest binary mismatch: {name}")
        if package_manifest.get("config_templates") != ["config/config.example.yaml", "config/.env.example"]:
            raise SystemExit(f"FAIL: CPA CLI package manifest config templates are incomplete: {name}")
        if package_manifest.get("launcher") != "run.sh" or package_manifest.get("runtime_checker") != "scripts/check-license-runtime.sh":
            raise SystemExit(f"FAIL: CPA CLI package manifest launcher/checker mismatch: {name}")
        expected_image = str(cpa_manifest_component.get("image", ""))
        expected_archive_path = "image/cli-proxy-api-cpa.tar" if package_has_image else None
        if package_manifest.get("image_load_ref") != expected_image:
            raise SystemExit(f"FAIL: CPA CLI package manifest image_load_ref mismatch: {name}")
        if package_manifest.get("image_archive") != expected_archive_path:
            raise SystemExit(f"FAIL: CPA CLI package manifest image_archive mismatch: {name}")
        if package_manifest.get("image_archive_tag") != (expected_image if package_has_image else None):
            raise SystemExit(f"FAIL: CPA CLI package manifest image_archive_tag mismatch: {name}")
        expected_package_contents = list(cpa_base_contents)
        if package_has_image:
            expected_package_contents.append("image/cli-proxy-api-cpa.tar")
        if package_manifest.get("contents") != expected_package_contents:
            raise SystemExit(f"FAIL: CPA CLI package manifest contents mismatch: {name}")
        binary_member = relative_members.get("bin/CLIProxyAPI")
        if binary_member is None or binary_member.size <= 0 or binary_member.mode & 0o111 == 0:
            raise SystemExit(f"FAIL: CPA CLI binary is missing or not executable ({name})")
        if package_has_image:
            inspect_image_archive(archive, relative_members.get("image/cli-proxy-api-cpa.tar"), expected_image, name)
        archive.close()

    # CPAMP bundles: verify both manager binaries, the copied panel bytes, the
    # deployment template, and the image archive's exact RepoTags. The panel
    # checksum is checked against actual bytes, not just a self-reported field.
    cpamp_base_required_files = {
        "package-manifest.json",
        "release-manifest.json",
        "release-catalog.json",
        "README.zh-CN.md",
        "management.html",
        "bin/cpa-manager-plus",
        "bin/cpamp-agent",
        "deploy/cpamp-pool-server/.env.example",
        "deploy/cpamp-pool-server/.gitignore",
        "deploy/cpamp-pool-server/README.md",
        "deploy/cpamp-pool-server/bootstrap.sh",
        "deploy/cpamp-pool-server/compose.yml",
        "deploy/cpamp-pool-server/config.yaml.template",
        "deploy/cpamp-pool-server/preflight.sh",
        "docs/deployment-cpa-cpamp.zh-CN.md",
    }
    for platform, name in expected_cpamp_artifacts.items():
        archive, members, relative_members, root = inspect_archive(artifact_dir / name, name)
        expected_root = f"CPAMP-{cpamp_manifest_component.get('version')}-{platform.replace('/', '-')}"
        if root != expected_root:
            archive.close()
            raise SystemExit(f"FAIL: CPAMP archive top-level directory mismatch ({name}): {root}")
        package_member = relative_members.get("package-manifest.json")
        if package_member is None:
            raise SystemExit(f"FAIL: CPAMP archive has no package-manifest.json: {name}")
        package_data = read_json_file_from_bytes(
            read_member_bytes(archive, package_member, name),
            f"CPAMP package manifest ({name})",
        )
        package_has_image = package_data.get("image_archive") is not None
        required = set(cpamp_base_required_files)
        if package_has_image:
            required.add("image/cpamp-image.tar")
        if not required.issubset(relative_members):
            missing = sorted(required - set(relative_members))
            raise SystemExit(f"FAIL: CPAMP archive is missing package files ({name}): {', '.join(missing)}")
        if not package_has_image and "image/cpamp-image.tar" in relative_members:
            raise SystemExit(f"FAIL: CPAMP package manifest omits an image archive that is present: {name}")
        if package_data.get("schema_version") != 1 or package_data.get("component") != "cpamp":
            raise SystemExit(f"FAIL: CPAMP package manifest schema/component mismatch: {name}")
        if package_data.get("platform") != platform:
            raise SystemExit(f"FAIL: CPAMP package manifest platform mismatch: {name}")
        for field in ("version", "image", "image_digest", "source_commit"):
            if package_data.get(field) != cpamp_manifest_component.get(field):
                raise SystemExit(f"FAIL: CPAMP package manifest {field} mismatch: {name}")
        expected_image = str(cpamp_manifest_component.get("image", ""))
        expected_platform_digest = (cpamp_manifest_component.get("platform_digests") or {}).get(platform)
        if package_data.get("platform_digest") != expected_platform_digest:
            raise SystemExit(f"FAIL: CPAMP package manifest platform_digest mismatch: {name}")
        if package_data.get("image_load_ref") != expected_image:
            raise SystemExit(f"FAIL: CPAMP package manifest image_load_ref mismatch: {name}")
        expected_archive_path = "image/cpamp-image.tar" if package_has_image else None
        if package_data.get("image_archive") != expected_archive_path:
            raise SystemExit(f"FAIL: CPAMP package manifest image_archive mismatch: {name}")
        if package_data.get("image_archive_tag") != (expected_image if package_has_image else None):
            raise SystemExit(f"FAIL: CPAMP package manifest image_archive_tag mismatch: {name}")
        expected_package_contents = list(cpamp_base_contents)
        if package_has_image:
            expected_package_contents.append("image/cpamp-image.tar")
        if package_data.get("contents") != expected_package_contents:
            raise SystemExit(f"FAIL: CPAMP package manifest contents mismatch: {name}")
        if package_data.get("panel_asset") != "management.html" or package_data.get("panel_asset_sha256") != panel_asset_sha256:
            raise SystemExit(f"FAIL: CPAMP package manifest panel_asset mismatch: {name}")
        panel_member = relative_members.get("management.html")
        if panel_member is None or hashlib.sha256(read_member_bytes(archive, panel_member, name)).hexdigest() != panel_asset_sha256:
            raise SystemExit(f"FAIL: CPAMP package management.html checksum mismatch: {name}")
        if package_data.get("binaries") != ["bin/cpa-manager-plus", "bin/cpamp-agent"]:
            raise SystemExit(f"FAIL: CPAMP package manifest binaries are incomplete: {name}")
        expected_template_files = [
            "deploy/cpamp-pool-server/.env.example",
            "deploy/cpamp-pool-server/.gitignore",
            "deploy/cpamp-pool-server/README.md",
            "deploy/cpamp-pool-server/bootstrap.sh",
            "deploy/cpamp-pool-server/compose.yml",
            "deploy/cpamp-pool-server/config.yaml.template",
            "deploy/cpamp-pool-server/preflight.sh",
        ]
        if package_data.get("template_files") != expected_template_files:
            raise SystemExit(f"FAIL: CPAMP package manifest template files are incomplete: {name}")
        template_member = relative_members.get("deploy/cpamp-pool-server/.env.example")
        if template_member is None:
            raise SystemExit(f"FAIL: CPAMP archive has no .env.example: {name}")
        template_lines = read_member_bytes(archive, template_member, name).decode("utf-8").splitlines()
        template_image = next(
            (line.split("=", 1)[1].strip().strip("\"'") for line in template_lines if line.startswith("CPAMP_IMAGE=")),
            "",
        )
        if template_image != expected_image:
            raise SystemExit(f"FAIL: CPAMP archive CPAMP_IMAGE does not match release image: {name}")
        for binary_path in ("bin/cpa-manager-plus", "bin/cpamp-agent"):
            binary_member = relative_members.get(binary_path)
            if binary_member is None or binary_member.size <= 0 or binary_member.mode & 0o111 == 0:
                raise SystemExit(f"FAIL: CPAMP binary is missing or not executable ({name}): {binary_path}")
        if package_has_image:
            inspect_image_archive(archive, relative_members.get("image/cpamp-image.tar"), expected_image, name)
        archive.close()
PY

forbidden="$(git ls-files | awk '
  function ends_with(path, suffix) {
    return length(path) >= length(suffix) && substr(path, length(path) - length(suffix) + 1) == suffix
  }
  {
    count = split($0, parts, "/")
    basename = parts[count]
    if (basename == ".env" || index($0, "/secrets/") || index($0, "/data/license/") || index($0, "/internal/api/data/") || ends_with($0, ".private") || ends_with($0, ".s2plugin")) {
      print
    }
  }
')"
if [[ -n "$forbidden" ]]; then
  echo "FAIL: forbidden customer-specific release paths are tracked:" >&2
  printf '%s\n' "$forbidden" >&2
  exit 1
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/cpa-release-verify.XXXXXX")"
cleanup() { rm -rf "$tmp_dir"; }
trap cleanup EXIT
secret_file="$tmp_dir/client-secret"
env_file="$tmp_dir/release.env"
printf '%s' 'release-verification-secret' > "$secret_file"
chmod 600 "$secret_file"
cp .env.example "$env_file"
cat >> "$env_file" <<EOF
MANAGEMENT_PASSWORD=release-verification-management-key
CPA_LICENSE_CLIENT_ID=release-verification
CPA_LICENSE_CLIENT_SECRET_HOST_PATH=$secret_file
CLI_PROXY_CONFIG_PATH=$repo_root/config.example.yaml
CLI_PROXY_AUTH_PATH=$tmp_dir/auths
CLI_PROXY_LOG_PATH=$tmp_dir/logs
CLI_PROXY_PLUGIN_PATH=$tmp_dir/plugins
CLI_PROXY_LICENSE_PATH=$tmp_dir/license
CLI_PROXY_PULL_POLICY=build
EOF
mkdir -p "$tmp_dir/auths" "$tmp_dir/logs" "$tmp_dir/plugins" "$tmp_dir/license"

docker compose --env-file "$env_file" -f docker-compose.yml config --quiet
bash scripts/check-license-deployment.sh --env-file "$env_file" --compose-file docker-compose.yml
echo "CPA release bundle verification passed (${expected_version:-manifest version})."
