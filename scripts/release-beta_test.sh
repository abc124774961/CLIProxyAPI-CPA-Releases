#!/usr/bin/env bash
set -Eeuo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
python3 - "$repo_root" <<'PY'
import os
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest

sys.dont_write_bytecode = True

ROOT = Path(sys.argv.pop())
BETA = "v7.2.148-cpa.7-beta.2"
STABLE = "v7.2.148-cpa.6"


def run(*args, cwd=None, env=None):
    return subprocess.run(args, cwd=cwd, env=env, text=True, capture_output=True)


class BetaReleaseTests(unittest.TestCase):
    def test_bash_version_gates(self):
        files = (
            ".github/workflows/cpa-release.yml", "install-cpa-release.sh",
            "install-cpa-cli-release.sh", "install-cpamp-release.sh",
            "scripts/package-cpa-release.sh", "scripts/package-cpamp-release.sh",
        )
        total = 0
        for name in files:
            patterns = re.findall(r"=~ (\^v\[0-9\].*?) \]\]", (ROOT / name).read_text())
            self.assertTrue(patterns, name)
            for pattern in patterns:
                total += 1
                for value in (STABLE, BETA, "v1.12.10-cpa.1-beta.20"):
                    with self.subTest(file=name, value=value):
                        self.assertEqual(run("bash", "-c", '[[ "$1" =~ $2 ]]', "test", value, pattern).returncode, 0)
                for value in ("latest", BETA + "/x", "v7.2.148-cpa.7-beta.0", "v7.2.148-cpa.7-beta.01", "v7.2.148-cpa.7-rc.1"):
                    with self.subTest(file=name, value=value):
                        self.assertNotEqual(run("bash", "-c", '[[ "$1" =~ $2 ]]', "test", value, pattern).returncode, 0)
        self.assertEqual(total, 8)

    def test_python_version_gates(self):
        patterns = re.findall(r're\.fullmatch\(r"(v\[0-9\][^"]+)"', (ROOT / "scripts/verify-release-bundle.sh").read_text())
        self.assertEqual(len(patterns), 2)
        for pattern in patterns:
            self.assertIsNotNone(re.fullmatch(pattern, STABLE))
            self.assertIsNotNone(re.fullmatch(pattern, BETA))
            self.assertIsNone(re.fullmatch(pattern, "v7.2.148-cpa.7-beta.0"))

    def test_cli_explicit_beta_dry_run(self):
        result = run("bash", str(ROOT / "install-cpa-cli-release.sh"), "--version", BETA, "--dry-run")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("/releases/download/" + BETA, result.stdout)

    def test_cpamp_explicit_beta_dry_run(self):
        result = run("bash", str(ROOT / "install-cpamp-release.sh"), "--version", BETA, "--dry-run")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("/releases/download/" + BETA, result.stdout)

    def test_installer_default_stays_stable(self):
        for name in ("install-cpa-cli-release.sh", "install-cpamp-release.sh", "install-cpa-release.sh"):
            text = (ROOT / name).read_text()
            self.assertIn(":-" + STABLE, text, name)

    def test_beta_source_build_is_not_used(self):
        env = dict(os.environ, CPA_RELEASE_VERSION=BETA)
        result = run("bash", str(ROOT / "install-cpa-release.sh"), env=env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("install-cpa-cli-release.sh --version " + BETA, result.stderr)

    def test_workflow_beta_is_draft_and_not_latest(self):
        text = (ROOT / ".github/workflows/cpa-release.yml").read_text()
        self.assertIn("release_flags=(--prerelease --latest=false)", text)
        self.assertIn("release_flags+=(--draft)", text)
        self.assertIn('gh release edit "$GITHUB_REF_NAME" "${release_flags[@]}"', text)
        self.assertIn('--generate-notes "${release_flags[@]}"', text)
        self.assertIn('scripts/package-public-release.sh "$version" "$archive"', text)
        self.assertIn("Beta release is already public; published assets are immutable.", text)
        self.assertIn("- import_prebuilt", text)

    def test_beta_archive_allowlist_and_stable_behavior(self):
        script = (ROOT / "scripts/package-public-release.sh").read_text()
        files = shlex.split(re.search(r"(?ms)^files=\(\n(.*?)^\)", script).group(1))
        with tempfile.TemporaryDirectory(prefix="cpa-beta-archive-") as directory:
            fixture = Path(directory)
            for name in files + ["internal/private.go", "auths/runtime.json", "secrets/runtime.key"]:
                target = fixture / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text("synthetic fixture\n")
            target_script = fixture / "scripts/package-public-release.sh"
            shutil.copyfile(ROOT / "scripts/package-public-release.sh", target_script)
            for command in (
                ("git", "init", "-q"),
                ("git", "add", "."),
                ("git", "-c", "user.name=Beta Test", "-c", "user.email=beta@example.test", "commit", "-qm", "fixture"),
                ("git", "tag", BETA), ("git", "tag", STABLE),
            ):
                result = run(*command, cwd=fixture)
                self.assertEqual(result.returncode, 0, result.stderr)
            for version in (BETA, STABLE):
                archive = fixture / (version + ".tar.gz")
                result = run("bash", str(target_script), version, str(archive))
                self.assertEqual(result.returncode, 0, result.stderr)
                with tarfile.open(archive) as package:
                    names = {"/".join(Path(member.name).parts[1:]) for member in package if member.isfile()}
                if version == BETA:
                    self.assertEqual(names, set(files))
                else:
                    self.assertIn("internal/private.go", names)

    def test_fixed_panel_directory_and_no_source_build(self):
        compose = (ROOT / "deploy/cpamp-pool-server/compose.yml").read_text()
        self.assertIn('PANEL_PATH: "/panel/management.html"', compose)
        self.assertIn('${CPAMP_PANEL_DIR:-./panel}:/panel:ro', compose)
        config = (ROOT / "deploy/cpamp-pool-server/config.yaml.template").read_text()
        self.assertIn("disable-auto-update-panel: true", config)
        root_compose = (ROOT / "docker-compose.yml").read_text()
        self.assertNotRegex(root_compose, r"(?m)^\s+build:")
        self.assertIn("pull_policy: ${CLI_PROXY_PULL_POLICY:-always}", root_compose)

    def test_bootstrap_panel_and_stale_panel_gate(self):
        with tempfile.TemporaryDirectory(prefix="cpa-beta-panel-") as directory:
            fixture = Path(directory)
            source = fixture / "release/deploy/cpamp-pool-server"
            shutil.copytree(ROOT / "deploy/cpamp-pool-server", source)
            panel = "<html>v1.12.10-cpa.1-beta.1</html>"
            (fixture / "release/management.html").write_text(panel)
            target = fixture / "stack"
            target.mkdir()
            result = run("bash", str(source / "bootstrap.sh"), "--dir", str(target), "--render")
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual((target / "panel/management.html").read_text(), panel)
            config = (target / "data/cpa/config.yaml").read_text()
            self.assertIn("disable-auto-update-panel: true", config)
            (target / "panel/management.html").write_text("<html>old version</html>")
            result = run("bash", str(source / "bootstrap.sh"), "--dir", str(target), "--render")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Bundled panel version must match", result.stderr)

    def test_image_import_stable_noop(self):
        result = run("bash", str(ROOT / "scripts/publish-beta-images.sh"), STABLE)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("prebuilt beta import is not used", result.stdout)

    def test_image_metadata_rejects_wrong_components_and_digests(self):
        spec = importlib.util.spec_from_file_location("beta_images", ROOT / "scripts/validate-beta-images.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory(prefix="cpa-beta-digests-") as directory:
            path = Path(directory) / "index.json"
            raw = json.dumps({"manifests": [
                {"platform": {"os": "linux", "architecture": "amd64"}, "digest": "sha256:" + "a" * 64},
                {"platform": {"os": "linux", "architecture": "arm64"}, "digest": "sha256:" + "b" * 64},
            ]}).encode()
            path.write_bytes(raw)
            digest = "sha256:" + hashlib.sha256(raw).hexdigest()
            module.verify(path, digest, "sha256:" + "a" * 64, "sha256:" + "b" * 64)
            with self.assertRaises(SystemExit):
                module.verify(path, "sha256:" + "0" * 64, "sha256:" + "a" * 64, "sha256:" + "b" * 64)
            with self.assertRaises(SystemExit):
                module.verify(path, digest, "sha256:" + "b" * 64, "sha256:" + "a" * 64)

    def test_image_import_fails_closed_and_preserves_existing_tags(self):
        with tempfile.TemporaryDirectory(prefix="cpa-beta-import-") as directory:
            fixture = Path(directory)
            (fixture / "scripts").mkdir()
            (fixture / "bin").mkdir()
            for name in ("publish-beta-images.sh", "validate-beta-images.py"):
                shutil.copyfile(ROOT / "scripts" / name, fixture / "scripts" / name)
            raw = json.dumps({"manifests": [
                {"platform": {"os": "linux", "architecture": "amd64"}, "digest": "sha256:" + "a" * 64},
                {"platform": {"os": "linux", "architecture": "arm64"}, "digest": "sha256:" + "b" * 64},
            ]})
            (fixture / "raw.json").write_text(raw)
            (fixture / "oci.tar").write_bytes(b"synthetic OCI fixture")
            images = ("cli-proxy-api-cpa:" + BETA, "cpa-manager-plus:v1.12.10-cpa.1-beta.1")
            entries = []
            components = {}
            for key, image in zip(("cpa_cli", "cpamp"), images):
                image = "ghcr.io/abc124774961/" + image
                entry = {
                    "image": image, "archive": key + ".tar",
                    "sha256": hashlib.sha256((fixture / "oci.tar").read_bytes()).hexdigest(),
                    "image_digest": "sha256:" + hashlib.sha256(raw.encode()).hexdigest(),
                    "source_commit": "c" * 40,
                    "platform_digests": {"linux/amd64": "sha256:" + "a" * 64, "linux/arm64": "sha256:" + "b" * 64},
                }
                entries.append(entry)
                components[key] = dict(entry, version=image.split(":")[-1], platforms=["linux/amd64", "linux/arm64"])
            (fixture / "beta-image-imports.json").write_text(json.dumps({"entries": entries}))
            (fixture / "release-manifest.json").write_text(json.dumps({"version": BETA, "components": components}))
            mock = '''#!/usr/bin/env python3
import json, os, pathlib, shutil, sys
root = pathlib.Path(os.environ["MOCK_ROOT"])
args = sys.argv[1:]
tool = pathlib.Path(sys.argv[0]).name
with (root / "calls").open("a") as log:
    log.write(tool + " " + " ".join(args) + "\\n")
if tool == "gh":
    shutil.copyfile(root / "oci.tar", pathlib.Path(args[args.index("--dir") + 1]) / args[args.index("--pattern") + 1])
elif args[0] == "login":
    sys.stdin.read()
elif args[0] == "copy":
    assert "--all" in args and "--preserve-digests" in args
    (root / "state").write_text("correct")
elif args[0] == "inspect" and "--raw" in args:
    if args[-1].startswith("docker:"):
        state = (root / "state").read_text()
        if state in ("absent", "error"):
            sys.stderr.write("manifest unknown" if state == "absent" else "authentication required")
            sys.exit(1)
        if state == "wrong":
            sys.stdout.write("{}")
            sys.exit(0)
    sys.stdout.write((root / "raw.json").read_text())
else:
    version = "v1.12.10-cpa.1-beta.1" if "cpa-manager-plus:" in args[-1] or "cpamp.tar" in args[-1] else "v7.2.148-cpa.7-beta.2"
    print(json.dumps({"Labels": {"org.opencontainers.image.source": "https://github.com/abc124774961/CLIProxyAPI-CPA-Releases", "org.opencontainers.image.revision": "c" * 40, "org.opencontainers.image.version": version}}))
'''
            for name in ("gh", "skopeo"):
                path = fixture / "bin" / name
                path.write_text(mock)
                path.chmod(0o755)
            env = dict(os.environ, MOCK_ROOT=str(fixture), RUNNER_TEMP=str(fixture),
                       GITHUB_REPOSITORY="abc124774961/CLIProxyAPI-CPA-Releases", GITHUB_ACTOR="beta-test",
                       GH_TOKEN="synthetic-local-test-token", PATH=str(fixture / "bin") + os.pathsep + os.environ["PATH"])
            for state in ("wrong", "error", "correct", "absent"):
                with self.subTest(state=state):
                    (fixture / "state").write_text(state)
                    (fixture / "calls").write_text("")
                    result = run("bash", str(fixture / "scripts/publish-beta-images.sh"), BETA, env=env)
                    calls = (fixture / "calls").read_text()
                    if state in ("wrong", "error"):
                        self.assertNotEqual(result.returncode, 0, result.stdout)
                        self.assertNotIn("skopeo copy", calls)
                        self.assertNotIn("gh release download", calls)
                    elif state == "correct":
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertNotIn("skopeo copy", calls)
                        self.assertNotIn("gh release download", calls)
                    else:
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertIn("skopeo copy --all --preserve-digests", calls)
                        self.assertIn("gh release download", calls)
                    self.assertNotIn("synthetic-local-test-token", result.stdout + result.stderr + calls)

    def test_cpamp_installer_download_to_fixed_panel(self):
        with tempfile.TemporaryDirectory(prefix="cpamp-beta-install-") as directory:
            fixture = Path(directory)
            assets = fixture / "assets"
            assets.mkdir()
            mock_bin = fixture / "bin"
            mock_bin.mkdir()
            version = "v1.12.10-cpa.1-beta.1"
            image = "ghcr.io/abc124774961/cpa-manager-plus:" + version
            package_name = "CPAMP-" + version + "-linux-amd64"
            package = fixture / "build" / package_name
            package.mkdir(parents=True)
            shutil.copytree(ROOT / "deploy/cpamp-pool-server", package / "deploy/cpamp-pool-server")
            (package / "bin").mkdir()
            for name in ("cpa-manager-plus", "cpamp-agent"):
                path = package / "bin" / name
                path.write_text("#!/bin/sh\nexit 0\n")
                path.chmod(0o755)
            panel = ("<html>" + version + "</html>").encode()
            (package / "management.html").write_bytes(panel)
            (package / "LICENSE").write_text("synthetic fixture")
            (package / "docs").mkdir()
            (package / "docs/deployment-cpa-cpamp.zh-CN.md").write_text("synthetic fixture")
            (package / "image").mkdir()
            templates = ["deploy/cpamp-pool-server/" + name for name in (".env.example", ".gitignore", "README.md", "bootstrap.sh", "compose.yml", "config.yaml.template", "preflight.sh")]
            base_manifest = {
                "schema_version": 1, "component": "cpamp", "version": version,
                "platform": "linux/amd64", "image": image, "image_load_ref": image,
                "image_digest": "sha256:" + "a" * 64, "platform_digest": "sha256:" + "b" * 64,
                "source_commit": "c" * 40, "binaries": ["bin/cpa-manager-plus", "bin/cpamp-agent"],
                "deployment_template": "deploy/cpamp-pool-server", "template_files": templates,
                "deployment_guide": "docs/deployment-cpa-cpamp.zh-CN.md",
                "panel_asset": "management.html", "panel_asset_sha256": hashlib.sha256(panel).hexdigest(),
                "image_archive": "image/cpamp-image.tar", "image_archive_tag": image,
                "contents": ["bin/cpa-manager-plus", "bin/cpamp-agent", "management.html", "deploy/cpamp-pool-server", "docs/deployment-cpa-cpamp.zh-CN.md", "image/cpamp-image.tar"],
            }
            component = dict(base_manifest, platform_digests={"linux/amd64": base_manifest["platform_digest"]})
            (assets / "release-manifest.json").write_text(json.dumps({"version": BETA, "components": {"cpamp": component}}))
            mock = '''#!/usr/bin/env python3
import json, os, pathlib, shutil, sys, urllib.parse
root = pathlib.Path(os.environ["MOCK_ROOT"])
args = sys.argv[1:]
name = pathlib.Path(sys.argv[0]).name
if name == "uname":
    print("Linux" if args == ["-s"] else "x86_64")
elif name == "curl":
    url = next(value for value in args if value.startswith("https://"))
    shutil.copyfile(root / "assets" / pathlib.Path(urllib.parse.urlparse(url).path).name, args[args.index("--output") + 1])
elif args[0] == "load":
    (root / "loaded").write_text("loaded")
elif args[0:2] == ["image", "inspect"]:
    print("amd64" if "Architecture" in args[args.index("--format") + 1] else json.dumps(["ghcr.io/abc124774961/cpa-manager-plus:v1.12.10-cpa.1-beta.1"]))
else:
    sys.exit("unexpected docker operation")
'''
            for name in ("uname", "curl", "docker"):
                path = mock_bin / name
                path.write_text(mock)
                path.chmod(0o755)
            env = dict(os.environ, MOCK_ROOT=str(fixture), PATH=str(mock_bin) + os.pathsep + os.environ["PATH"])
            for variant in ("wrong_digest", "wrong_tag", "wrong_arch", "wrong_panel", "valid"):
                with self.subTest(variant=variant):
                    manifest = dict(base_manifest)
                    if variant == "wrong_digest":
                        manifest["image_digest"] = "sha256:" + "d" * 64
                    (package / "package-manifest.json").write_text(json.dumps(manifest))
                    (package / "management.html").write_bytes(b"old panel" if variant == "wrong_panel" else panel)
                    tag = "ghcr.io/abc124774961/cpa-manager-plus:wrong" if variant == "wrong_tag" else image
                    arch = "arm64" if variant == "wrong_arch" else "amd64"
                    with tarfile.open(package / "image/cpamp-image.tar", "w") as image_archive:
                        for name, data in (("manifest.json", [{"RepoTags": [tag], "Config": "config.json", "Layers": []}]), ("config.json", {"os": "linux", "architecture": arch})):
                            body = json.dumps(data).encode()
                            member = tarfile.TarInfo(name)
                            member.size = len(body)
                            image_archive.addfile(member, io.BytesIO(body))
                    archive = assets / (package_name + ".tar.gz")
                    with tarfile.open(archive, "w:gz") as bundle:
                        bundle.add(package, arcname=package_name)
                    (assets / "checksums.txt").write_text(hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n")
                    target = fixture / ("installed-" + variant)
                    result = run("bash", str(ROOT / "install-cpamp-release.sh"), "--version", BETA, "--dir", str(target), "--load-image", env=env)
                    if variant != "valid":
                        self.assertNotEqual(result.returncode, 0)
                        self.assertFalse(target.exists())
                        self.assertFalse((fixture / "loaded").exists())
                    else:
                        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                        self.assertTrue((fixture / "loaded").exists())
                        result = run("bash", str(target / "deploy/cpamp-pool-server/bootstrap.sh"), "--render", env=env)
                        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                        self.assertEqual((target / "deploy/cpamp-pool-server/panel/management.html").read_bytes(), panel)


unittest.main(verbosity=2)
PY
