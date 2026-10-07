"""Release notes must come only from the selected reviewed changelog section."""
import unittest
import os
import hashlib
import json
import posixpath
from pathlib import Path
import re
import tempfile
import textwrap
from unittest.mock import patch

from release_notes import extract_release_notes, release_metadata
from release_registry import RegistryError, check_absent, check_published


class ReleaseNotesTest(unittest.TestCase):
    def test_stable_publication_uses_reviewed_stable_notes(self):
        metadata = release_metadata("v1.0.0")
        self.assertEqual(metadata, {"version": "1.0.0", "prerelease": False,
                                    "latest": True, "release_stage": "release"})
        source = "## Unreleased\nfuture\n## 1.0.0\nApproved stable notes.\n## 1.0.0-rc.1\nCandidate notes.\n"
        self.assertEqual(extract_release_notes(source, metadata["version"]), "Approved stable notes.\n")

    def test_prerelease_publication_never_becomes_latest(self):
        for stage in ("alpha", "beta", "rc"):
            with self.subTest(stage=stage):
                self.assertEqual(release_metadata(f"v1.0.0-{stage}.2"),
                                 {"version": f"1.0.0-{stage}.2", "prerelease": True,
                                  "latest": False, "release_stage": stage})

    def test_publication_requires_an_explicit_canonical_tag(self):
        invalid = ("1.0.0", "v1", "v1.0", "v01.0.0", "v1.00.0", "v1.0.00", "v1.0.0\n",
                   "v1.0.0-alpha.0", "v1.0.0-rc.01", "v1.0.0-nightly.1", "v1.0.0+build.1",
                   "main", "refs/tags/v1.0.0", "../v1.0.0", "v1.0.0; echo unsafe")
        for tag in invalid:
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                release_metadata(tag)

    def test_exact_version_preserves_body_and_stops_at_next_section(self):
        source = "# Changelog\n\n## Unreleased\nfuture\n\n## 1.2.0-alpha.1 — today\n\nFirst.\n\n### Fixes\n\n- fixed\n\n## 1.1.0 — yesterday\nold\n"
        expected = "First.\n\n### Fixes\n\n- fixed\n"
        self.assertEqual(extract_release_notes(source, "v1.2.0-alpha.1"), expected)
        self.assertEqual(extract_release_notes(source, "1.2.0-alpha.1"), expected)

    def test_fenced_headings_are_not_sections(self):
        source = "## [1.2.0] - today\nExample:\n```md\n## 1.2.0\n```\n~~~md\n## Unreleased\n~~~\nEnd.\n## 1.1.0\nold\n"
        self.assertIn("## Unreleased\n~~~\nEnd.", extract_release_notes(source, "1.2.0"))

    def test_missing_empty_duplicate_and_invalid_versions_fail(self):
        cases = [
            ("## Unreleased\nnot reviewed", "1.2.0"),
            ("## 1.2.0\n\n## 1.1.0\nold", "1.2.0"),
            ("## 1.2.0\none\n## [v1.2.0]\ntwo", "1.2.0"),
            ("## 1.2.0-alpha.10\nother", "1.2.0-alpha.1"),
            ("## 1.2.0\nbody", "Unreleased"),
            ("## 1.2.0\nbody", "../1.2.0"),
        ]
        for source, version in cases:
            with self.subTest(source=source, version=version), self.assertRaises(ValueError):
                extract_release_notes(source, version)


class PublicationWorkflowTest(unittest.TestCase):
    """Exercise the actual workflow publisher without contacting GitHub."""

    def run_publisher(self, tag, remote_sha="a" * 40):
        source = (Path(__file__).resolve().parents[1] / ".github/workflows/release.yml").read_text()
        publisher = textwrap.dedent(re.findall(r"          python3 - <<'PY'\n(.*?)\n          PY", source, re.DOTALL)[-1])
        with tempfile.TemporaryDirectory() as temporary:
            environment = {"RELEASE_TAG": tag, "IMAGE_DIGEST": "sha256:" + "b" * 64,
                           "IMAGE": "ghcr.io/example/gobale", "GITHUB_REPOSITORY": "example/gobale",
                           "DOCKERHUB_IMAGE": "docker.io/mimalef70/gobale",
                           "RUNNER_TEMP": temporary, "EXPECTED_SHA": "a" * 40}
            with patch.dict(os.environ, environment), \
                 patch("pathlib.Path.glob", return_value=[Path(f"dist/archive-{index}.tar.gz") for index in range(4)]), \
                 patch("subprocess.check_output", side_effect=["Reviewed notes.\n", remote_sha + "\n"]), \
                 patch("subprocess.run") as run:
                try:
                    exec(compile(publisher, "release.yml publisher", "exec"), {})
                except AssertionError:
                    run.assert_not_called()
                    raise
                self.notes = (Path(temporary) / 'gobale-release-notes.md').read_text()
                return [call.args[0] for call in run.call_args_list]

    def test_stable_publisher_marks_release_not_prerelease(self):
        commands = self.run_publisher("v1.0.0")
        self.assertEqual([command[2] for command in commands], ["create", "upload", "edit"])
        for command in (commands[0], commands[-1]):
            self.assertIn("--prerelease=false", command)
        self.assertIn("--latest=false", commands[0])
        self.assertIn("--latest=true", commands[-1])
        self.assertIn("--verify-tag", commands[0])
        self.assertIn("--draft", commands[0])
        self.assertIn("--draft=false", commands[-1])
        self.assertFalse(any("--clobber" in command for command in commands))
        for image in ("ghcr.io/example/gobale", "docker.io/mimalef70/gobale"):
            self.assertIn(image + ":v1.0.0", self.notes)
            self.assertIn(image + "@sha256:" + "b" * 64, self.notes)

    def test_candidate_publisher_cannot_become_latest(self):
        commands = self.run_publisher("v1.0.0-rc.1")
        for command in (commands[0], commands[-1]):
            self.assertIn("--prerelease=true", command)
            self.assertIn("--latest=false", command)

    def test_tag_move_aborts_before_creating_release(self):
        with self.assertRaisesRegex(AssertionError, "Version tag moved"):
            self.run_publisher("v1.0.0", remote_sha="c" * 40)

    def test_one_build_publishes_both_registries_after_read_only_preflight(self):
        import yaml
        source = (Path(__file__).resolve().parents[1] / ".github/workflows/release.yml").read_text()
        jobs = yaml.safe_load(source)["jobs"]
        prepare = jobs["prepare"]["steps"]
        absence = next(index for index, step in enumerate(prepare)
                       if "release_registry.py absent" in step.get("run", ""))
        package = next(index for index, step in enumerate(prepare)
                       if "package_release.py" in step.get("run", ""))
        self.assertLess(absence, package)
        self.assertIn("DOCKERHUB_TOKEN", prepare[0]["env"])
        steps = jobs["image"]["steps"]
        builds = [(index, step) for index, step in enumerate(steps)
                  if step.get("uses", "").startswith("docker/build-push-action@")]
        self.assertEqual(len(builds), 1)
        build_index, build = builds[0]
        options = build["with"]
        self.assertEqual(options["platforms"], "linux/amd64,linux/arm64")
        self.assertEqual(options["provenance"], "mode=max")
        self.assertTrue(options["push"])
        self.assertTrue(options["sbom"])
        self.assertEqual(options["tags"].strip().splitlines(), [
            "${{ env.GHCR_IMAGE }}:${{ inputs.tag }}",
            "${{ env.DOCKERHUB_IMAGE }}:${{ inputs.tag }}"])
        self.assertIn("release_registry.py absent", steps[build_index - 1]["run"])
        self.assertIn("release_registry.py published", steps[build_index + 1]["run"])
        logins = [step for step in steps if step.get("uses", "").startswith("docker/login-action@")]
        self.assertEqual({step["with"]["registry"] for step in logins}, {"ghcr.io", "docker.io"})
        for step in logins:
            self.assertEqual(step["uses"], "docker/login-action@dbcb813823bdd20940b903addbd779551569679f")

    def test_secret_username_cannot_suppress_public_image_names_between_jobs(self):
        import yaml
        source = (Path(__file__).resolve().parents[1] / ".github/workflows/release.yml").read_text()
        workflow = yaml.safe_load(source)
        jobs = workflow["jobs"]
        # Actions suppresses any job output containing a configured secret's
        # value, including the public Docker Hub username. No image name may
        # depend on that output transport, even though it is safe to log masked.
        self.assertEqual(set(jobs["prepare"]["outputs"]), {"sha", "version"})
        self.assertNotIn("needs.prepare.outputs.image", source)
        self.assertNotIn("needs.prepare.outputs.dockerhub_image", source)
        self.assertEqual(workflow["env"]["GHCR_IMAGE"], "ghcr.io/mimalef70/gobale")
        self.assertEqual(workflow["env"]["DOCKERHUB_IMAGE"], "docker.io/mimalef70/gobale")
        build = next(step for step in jobs["image"]["steps"] if step.get("id") == "build")
        tags = build["with"]["tags"].replace("${{ inputs.tag }}", "v2.0.0")
        for key in ("GHCR_IMAGE", "DOCKERHUB_IMAGE"):
            tags = tags.replace("${{ env." + key + " }}", workflow["env"][key])
        self.assertEqual(tags.strip().splitlines(), ["ghcr.io/mimalef70/gobale:v2.0.0",
                                                  "docker.io/mimalef70/gobale:v2.0.0"])
        publisher = jobs["release"]["steps"][-1]["env"]
        self.assertEqual(publisher["IMAGE"], "${{ env.GHCR_IMAGE }}")
        self.assertEqual(publisher["DOCKERHUB_IMAGE"], "${{ env.DOCKERHUB_IMAGE }}")


class RegistryPublicationTest(unittest.TestCase):
    def setUp(self):
        self.environment = {"GITHUB_REPOSITORY": "example/gobale", "GITHUB_ACTOR": "fixture",
                            "GH_TOKEN": "github-secret", "DOCKERHUB_USERNAME": "fixture",
                            "DOCKERHUB_TOKEN": "docker-secret"}
        self.token = (200, {}, b'{"token":"synthetic-registry-token"}')
        self.absent = (404, {}, b'{"errors":[{"code":"MANIFEST_UNKNOWN"}]}')

    def test_absence_requires_authenticated_explicit_missing_manifest_on_both(self):
        with patch("release_registry.request", side_effect=[self.token, self.absent] * 2) as request:
            check_absent("v2.0.0", self.environment)
        calls = request.call_args_list
        self.assertEqual(len(calls), 4)
        self.assertTrue(calls[0].args[0].startswith("https://ghcr.io/token?"))
        self.assertEqual(calls[1].args[0], "https://ghcr.io/v2/example/gobale/manifests/v2.0.0")
        self.assertTrue(calls[2].args[0].startswith("https://auth.docker.io/token?"))
        self.assertEqual(calls[3].args[0], "https://registry-1.docker.io/v2/mimalef70/gobale/manifests/v2.0.0")
        for call in calls:
            self.assertNotIn("secret", call.args[0])

    def test_missing_credentials_fail_before_any_registry_request(self):
        for key in self.environment:
            with self.subTest(key=key), patch("release_registry.request") as request:
                with self.assertRaisesRegex(RegistryError, "required"):
                    check_absent("v2.0.0", dict(self.environment, **{key: ""}))
                request.assert_not_called()

    def test_existing_version_in_either_registry_blocks_retry(self):
        for responses in ([self.token, (200, {}, b"existing")],
                          [self.token, self.absent, self.token, (200, {}, b"existing")]):
            with self.subTest(registry=len(responses)), patch("release_registry.request", side_effect=responses):
                with self.assertRaisesRegex(RegistryError, "already exists"):
                    check_absent("v2.0.0", self.environment)

    def test_network_auth_and_ambiguous_missing_responses_never_count_as_absent(self):
        failures = [(status, {}, b'{"errors":[{"code":"MANIFEST_UNKNOWN"}]}')
                    for status in (301, 401, 403, 429, 500)]
        failures += [(404, {}, b"untrusted registry response"),
                     (404, {}, b'{"errors":[{"code":"UNAUTHORIZED"}]}'),
                     (404, {}, b'{"errors":[]}')]
        for failure in failures:
            with self.subTest(failure=failure), patch("release_registry.request", side_effect=[self.token, failure]):
                with self.assertRaisesRegex(RegistryError, "unverified"):
                    check_absent("v2.0.0", self.environment)
        with patch("release_registry.request", side_effect=RegistryError("Registry request failed")):
            with self.assertRaisesRegex(RegistryError, "request failed"):
                check_absent("v2.0.0", self.environment)

    def test_bad_authentication_never_exposes_the_response_or_secret(self):
        for response in ((401, {}, b"docker-secret"), (302, {}, b"github-secret"),
                         (200, {}, b'{"token":""}'), (200, {}, b"invalid")):
            with self.subTest(response=response), patch("release_registry.request", return_value=response):
                with self.assertRaises(RegistryError) as raised:
                    check_absent("v2.0.0", self.environment)
                self.assertNotIn("secret", str(raised.exception))

    def test_both_published_indexes_must_match_one_digest_and_both_architectures(self):
        raw = json.dumps({"manifests": [{"platform": {"os": "linux", "architecture": arch}}
                                         for arch in ("amd64", "arm64")]}).encode()
        digest = "sha256:" + hashlib.sha256(raw).hexdigest()
        manifest = (200, {"Docker-Content-Digest": digest}, raw)
        with patch("release_registry.request", side_effect=[self.token, manifest] * 2):
            check_published("v2.0.0", digest, self.environment)
        with patch("release_registry.request", side_effect=[self.token, manifest, self.token, (200, {}, raw + b" ")]):
            with self.assertRaisesRegex(RegistryError, "digest"):
                check_published("v2.0.0", digest, self.environment)
        with patch("release_registry.request", side_effect=[self.token, (200, {"Docker-Content-Digest": "sha256:" + "a" * 64}, raw)]):
            with self.assertRaisesRegex(RegistryError, "digest"):
                check_published("v2.0.0", digest, self.environment)

    def test_single_platform_manifest_cannot_be_published_as_multiarch(self):
        raw = b'{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}}]}'
        digest = "sha256:" + hashlib.sha256(raw).hexdigest()
        with patch("release_registry.request", side_effect=[self.token, (200, {}, raw)]):
            with self.assertRaisesRegex(RegistryError, "both supported"):
                check_published("v2.0.0", digest, self.environment)


class ReleasePackageDocumentationTest(unittest.TestCase):
    def test_all_shipped_markdown_file_links_have_shipped_targets(self):
        from package_release import ROOT, approved_files
        files = approved_files()
        names = {path.relative_to(ROOT).as_posix() for path in files}
        self.assertTrue({"AGENTS.md", "ui/README.md",
                         "src/internal/balemeow/testdata/coverage/capabilities.json"} <= names)
        for path in files:
            if path.suffix != ".md":
                continue
            name = path.relative_to(ROOT).as_posix()
            for target in re.findall(r"\]\(([^\s)]+)\)", path.read_text()):
                if target.startswith(("https:", "http:", "mailto:", "#")):
                    continue
                destination = posixpath.normpath(posixpath.join(posixpath.dirname(name), target.split("#")[0]))
                with self.subTest(source=name, target=target):
                    self.assertIn(destination, names)


if __name__ == "__main__":
    unittest.main()
