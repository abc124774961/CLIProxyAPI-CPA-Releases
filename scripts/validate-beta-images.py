#!/usr/bin/env python3
"""Validate public beta image metadata before registry publication."""
import hashlib
import json
from pathlib import Path
import re
import sys


def require(condition, message):
    if not condition:
        raise SystemExit("ERROR: " + message)


def plan(import_path, manifest_path, release):
    manifest = json.loads(Path(manifest_path).read_text())
    imports = json.loads(Path(import_path).read_text())
    require(re.fullmatch(r"v\d+\.\d+\.\d+-cpa\.\d+-beta\.[1-9]\d*", release), "expected an explicit beta release")
    require(manifest.get("version") == release, "release manifest version mismatch")
    entries = imports.get("entries", [])
    require(isinstance(entries, list) and len(entries) == 2, "expected both beta components exactly once")
    components = manifest.get("components", {})
    expected = {components.get(key, {}).get("image"): components.get(key, {}) for key in ("cpa_cli", "cpamp")}
    require(len(expected) == 2 and None not in expected, "release manifest must name two component images")
    seen = set()
    records = []
    for entry in entries:
        image = entry.get("image", "")
        require(image in expected and image not in seen, "unexpected or duplicate component image")
        seen.add(image)
        component = expected[image]
        require(re.fullmatch(r"ghcr\.io/abc124774961/(?:cli-proxy-api-cpa|cpa-manager-plus):v\d+\.\d+\.\d+-cpa\.\d+-beta\.[1-9]\d*", image), "image must use the public repository and an immutable beta tag")
        require(image.endswith(":" + component.get("version", "")), "component version/image mismatch")
        require(component.get("platforms") == ["linux/amd64", "linux/arm64"], "expected both Linux platforms")
        for field in ("image_digest", "source_commit", "platform_digests"):
            require(entry.get(field) == component.get(field), "image imports disagree with release manifest: " + field)
        require(re.fullmatch(r"sha256:[0-9a-f]{64}", entry.get("image_digest", "")), "invalid image index digest")
        require(re.fullmatch(r"[0-9a-f]{40}", entry.get("source_commit", "")), "invalid source commit")
        platforms = entry.get("platform_digests", {})
        require(set(platforms) == {"linux/amd64", "linux/arm64"}, "expected two platform digests")
        require(all(re.fullmatch(r"sha256:[0-9a-f]{64}", value) for value in platforms.values()), "invalid platform digest")
        archive = entry.get("archive", "")
        require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*\.tar", archive), "invalid OCI archive filename")
        require(re.fullmatch(r"[0-9a-f]{64}", entry.get("sha256", "")), "invalid archive checksum")
        records.append((image, archive, entry["sha256"], entry["image_digest"], entry["source_commit"], platforms["linux/amd64"], platforms["linux/arm64"]))
    for record in records:
        print("\t".join(record))


def verify(raw_path, digest, amd64, arm64):
    raw = Path(raw_path).read_bytes()
    require("sha256:" + hashlib.sha256(raw).hexdigest() == digest, "registry/OCI index digest mismatch")
    manifest = json.loads(raw)
    found = {}
    for item in manifest.get("manifests", []):
        platform = item.get("platform", {})
        key = platform.get("os", "") + "/" + platform.get("architecture", "")
        if key in ("linux/amd64", "linux/arm64"):
            require(key not in found, "duplicate platform in image index")
            found[key] = item.get("digest")
    require(found == {"linux/amd64": amd64, "linux/arm64": arm64}, "registry/OCI platform digests mismatch")


def labels(path, revision, version):
    data = json.loads(Path(path).read_text())
    values = data.get("Labels") or {}
    require(values.get("org.opencontainers.image.source") == "https://github.com/abc124774961/CLIProxyAPI-CPA-Releases", "image source label must identify the public release repository")
    require(values.get("org.opencontainers.image.revision") == revision, "image source revision label mismatch")
    require(values.get("org.opencontainers.image.version") == version, "image component version label mismatch")


if __name__ == "__main__":
    commands = {"plan": plan, "verify": verify, "labels": labels}
    require(len(sys.argv) > 1 and sys.argv[1] in commands, "expected plan, verify, or labels")
    commands[sys.argv[1]](*sys.argv[2:])
