"""Release notes must come only from the selected reviewed changelog section."""
import unittest
import os
from pathlib import Path
import re
import tempfile
import textwrap
from unittest.mock import patch

from release_notes import extract_release_notes, release_metadata


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

    def test_candidate_publisher_cannot_become_latest(self):
        commands = self.run_publisher("v1.0.0-rc.1")
        for command in (commands[0], commands[-1]):
            self.assertIn("--prerelease=true", command)
            self.assertIn("--latest=false", command)

    def test_tag_move_aborts_before_creating_release(self):
        with self.assertRaisesRegex(AssertionError, "Version tag moved"):
            self.run_publisher("v1.0.0", remote_sha="c" * 40)


if __name__ == "__main__":
    unittest.main()
