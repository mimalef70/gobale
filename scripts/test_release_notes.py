"""Release notes must come only from the selected reviewed changelog section."""
import unittest

from release_notes import extract_release_notes


class ReleaseNotesTest(unittest.TestCase):
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


if __name__ == "__main__":
    unittest.main()
