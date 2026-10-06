#!/usr/bin/env python3
"""Extract one reviewed version from CHANGELOG.md for prerelease publication."""
import argparse
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[1]
VERSION = r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?"
VERSION_HEADING = re.compile(r"^##\s+(?:\[(v?" + VERSION + r")\]|(v?" + VERSION + r"))(?=\s|$)")


def extract_release_notes(changelog: str, version: str) -> str:
    """Return the exact section body; never fall back to Unreleased or another tag."""
    selected = version.removeprefix("v")
    if not re.fullmatch(VERSION, selected):
        raise ValueError("version must be an explicit semantic version")
    matches = []
    section = None
    fence = None
    for line in changelog.splitlines(keepends=True):
        marker = re.match(r"^ {0,3}(`{3,}|~{3,})", line)
        if marker:
            token = marker[1]
            if fence is None:
                fence = token
            elif token[0] == fence[0] and len(token) >= len(fence):
                fence = None
            if section is not None:
                section.append(line)
            continue
        if fence is None and re.match(r"^##\s+", line):
            section = None
            heading = VERSION_HEADING.match(line)
            if heading and (heading[1] or heading[2]).removeprefix("v") == selected:
                section = []
                matches.append(section)
            continue
        if section is not None:
            section.append(line)
    if len(matches) != 1:
        raise ValueError(f"expected exactly one CHANGELOG section for {selected}; found {len(matches)}")
    notes = "".join(matches[0]).strip()
    if not notes:
        raise ValueError(f"CHANGELOG section for {selected} is empty")
    return notes + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, help="Exact version, with or without a v prefix")
    parser.add_argument("--changelog", type=Path, default=ROOT / "CHANGELOG.md")
    parser.add_argument("--output", type=Path, help="Write to a file instead of stdout")
    args = parser.parse_args()
    try:
        notes = extract_release_notes(args.changelog.read_text(), args.version)
    except (OSError, ValueError) as error:
        parser.exit(1, f"Release notes: {error}\n")
    if args.output:
        args.output.write_text(notes)
    else:
        sys.stdout.write(notes)


if __name__ == "__main__":
    main()
