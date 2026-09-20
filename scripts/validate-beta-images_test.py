#!/usr/bin/env python3
"""Exercise stable/beta import metadata and the draft-only publication gate."""
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import textwrap
import unittest


ROOT = Path(__file__).resolve().parents[1]
sys.dont_write_bytecode = True
SPEC = importlib.util.spec_from_file_location("release_images", ROOT / "scripts/validate-beta-images.py")
VALIDATOR = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(VALIDATOR)
STABLE = "v7.2.148-cpa.7"
BETA = STABLE + "-beta.2"


def metadata(release):
    components = {}
    entries = []
    for key, repo, version in (("cpa_cli", "cli-proxy-api-cpa", release),
                               ("cpamp", "cpa-manager-plus", "v1.12.10-cpa.1")):
        entry = dict(image="ghcr.io/abc124774961/" + repo + ":" + version,
                     archive=key + ".tar", sha256="d" * 64,
                     image_digest="sha256:" + "a" * 64, source_commit="c" * 40,
                     platform_digests={"linux/amd64": "sha256:" + "b" * 64,
                                       "linux/arm64": "sha256:" + "e" * 64})
        entries.append(entry)
        components[key] = dict(entry, version=version, platforms=["linux/amd64", "linux/arm64"])
    return (dict(version=release, repository=VALIDATOR.PUBLIC_REPOSITORY, components=components),
            dict(schema_version=1, release_tag=release, entries=entries))


def workflow_step(name):
    workflow = (ROOT / ".github/workflows/cpa-release.yml").read_text()
    block = workflow.split("      - name: " + name + "\n", 1)[1].split("\n      - name:", 1)[0]
    return textwrap.dedent(block.split("        run: |\n", 1)[1])


class ImportMetadataTests(unittest.TestCase):
    def evaluate(self, manifest, imports, release):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "manifest.json").write_text(json.dumps(manifest))
            (root / "imports.json").write_text(json.dumps(imports))
            with contextlib.redirect_stdout(io.StringIO()) as output:
                VALIDATOR.plan(root / "imports.json", root / "manifest.json", release)
            return output.getvalue()

    def test_stable_and_beta_import_both_components(self):
        for release in (STABLE, BETA):
            with self.subTest(release=release):
                manifest, imports = metadata(release)
                records = self.evaluate(manifest, imports, release).splitlines()
                self.assertEqual(len(records), 2)
                self.assertTrue(all(len(record.split("\t")) == 7 for record in records))

    def test_wrong_release_channel_repository_and_archive_rejected(self):
        cases = ("schema", "release", "repository", "duplicate-archive", "mismatched-digest", "stable-beta")
        for case in cases:
            with self.subTest(case=case):
                manifest, imports = metadata(STABLE)
                if case == "schema":
                    imports["schema_version"] = 2
                elif case == "release":
                    imports["release_tag"] = BETA
                elif case == "repository":
                    manifest["repository"] = "unrelated/repository"
                elif case == "duplicate-archive":
                    imports["entries"][1]["archive"] = imports["entries"][0]["archive"]
                elif case == "mismatched-digest":
                    imports["entries"][0]["image_digest"] = "sha256:" + "f" * 64
                else:
                    component = manifest["components"]["cpamp"]
                    component["version"] += "-beta.1"
                    component["image"] += "-beta.1"
                    imports["entries"][1]["image"] = component["image"]
                with self.assertRaises(SystemExit):
                    self.evaluate(manifest, imports, STABLE)


class PublicationGateTests(unittest.TestCase):
    def test_both_channels_stay_draft_and_public_releases_are_immutable(self):
        mock = '''#!/usr/bin/env python3
import json, os, pathlib, sys
args = sys.argv[1:]
with pathlib.Path(os.environ["CALLS"]).open("a") as handle:
    handle.write(json.dumps(args) + "\\n")
if args[:2] == ["release", "view"]:
    if os.environ["RELEASE_STATE"] == "missing":
        sys.exit(1)
    if "--json" in args:
        print("true" if os.environ["RELEASE_STATE"] == "draft" else "false")
'''
        script = workflow_step("Create or update draft GitHub release")
        for release in (STABLE, BETA):
            for state in ("draft", "missing", "published"):
                with self.subTest(release=release, state=state), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    (root / "gh").write_text(mock)
                    (root / "gh").chmod(0o755)
                    (root / "dist").mkdir()
                    (root / "dist/asset.txt").write_text("fixture")
                    calls = root / "calls.jsonl"
                    env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"],
                               CALLS=str(calls), GITHUB_REF_NAME=release, RELEASE_STATE=state)
                    result = subprocess.run(["bash", "-c", script], cwd=root, env=env, capture_output=True, text=True)
                    operations = [json.loads(line) for line in calls.read_text().splitlines()]
                    writes = [args for args in operations if args[1] in ("create", "edit", "upload")]
                    if state == "published":
                        self.assertNotEqual(result.returncode, 0)
                        self.assertFalse(writes)
                        continue
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertTrue(writes)
                    mutation = writes[0]
                    self.assertIn("--latest=false", mutation)
                    self.assertIn("--prerelease" if release == BETA else "--prerelease=false", mutation)
                    self.assertNotIn("--draft=false", mutation)
                    if state == "missing":
                        self.assertIn("--draft", mutation)
                        self.assertIn("--verify-tag", mutation)

    def test_uploaded_digest_size_and_draft_verified(self):
        script = workflow_step("Verify uploaded draft assets")
        code = script.split("<<'PY'\n", 1)[1].rsplit("\nPY", 1)[0]
        for failure in (None, "digest", "size", "draft", "channel", "missing"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / "dist").mkdir()
                payload = b"release asset"
                (root / "dist/asset.tar").write_bytes(payload)
                release = dict(draft=True, tag_name=STABLE, prerelease=False)
                asset = dict(name="asset.tar", state="uploaded", size=len(payload),
                             digest="sha256:" + hashlib.sha256(payload).hexdigest())
                if failure in ("digest", "size"):
                    asset[failure] = "wrong"
                elif failure == "draft":
                    release["draft"] = False
                elif failure == "channel":
                    release["prerelease"] = True
                (root / "release.json").write_text(json.dumps(release))
                (root / "assets.json").write_text(json.dumps([[] if failure == "missing" else [asset]]))
                result = subprocess.run(["python3", "-", "release.json", "assets.json"], input=code,
                                        cwd=root, env=dict(os.environ, GITHUB_REF_NAME=STABLE), capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, failure is None, result.stderr)


if __name__ == "__main__":
    unittest.main()
