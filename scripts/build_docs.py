#!/usr/bin/env python3
"""Stage the static API reference. No application runtime or account data needed."""
from pathlib import Path
import argparse
import json
import posixpath
import re
import shutil
import yaml

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--output", default="site")
args = parser.parse_args()
out = Path(args.output).resolve()
if out == root or any(out.is_relative_to(root / name) for name in ("src", "docs", "assets", ".git")):
    raise SystemExit("Choose a separate staging directory, not a source directory")
out.mkdir(parents=True, exist_ok=True)
expected = {"index.html", "style.css", "app.js", "icon.svg", "openapi.yaml", "openapi.json", ".nojekyll"}
guide_files = {"providers/sources.md", "providers/acceptance.md", "providers/eitaa-inventory.json",
               "providers/rubika-inventory.json", "providers/live-20261010.json",
               "upgrade-goomni.md", "consumer-integration.md", "operations.md", "webhook-payload.md"}
expected |= guide_files | {"THIRD_PARTY_NOTICES.md"}
if any(path.is_symlink() or (path.is_dir() and path.relative_to(out).as_posix() != "providers") or (path.is_file() and path.relative_to(out).as_posix() not in expected) for path in out.rglob("*")):
    raise SystemExit("Staging directory contains unrelated files; choose an empty output directory")
spec = yaml.safe_load((root / "docs/openapi.yaml").read_text())
assert spec["openapi"].startswith("3.") and spec["paths"]
for name in ("index.html", "style.css", "app.js", "icon.svg"):
    shutil.copyfile(root / "docs/site" / name, out / name)
shutil.copyfile(root / "docs/openapi.yaml", out / "openapi.yaml")
# Preserve the published JSON URL without tracking a duplicate source contract.
(out / "openapi.json").write_text(json.dumps(spec, ensure_ascii=False, indent=2) + "\n")
(out / ".nojekyll").touch()
(out / "providers").mkdir(exist_ok=True)
for name in guide_files:
    source = root / "docs" / name
    if source.suffix == ".md":
        # These guides are also downloadable from Pages, outside their original
        # repository directory. Resolve source-relative links to reviewed GitHub
        # paths instead of publishing links that escape the Pages project root.
        def published_link(match):
            target = match[1]
            if target.startswith(("https:", "http:", "mailto:", "#")):
                return match[0]
            path, separator, anchor = target.partition("#")
            resolved = posixpath.normpath(posixpath.join("docs", posixpath.dirname(name), path))
            return "](https://github.com/mimalef70/goomni/blob/main/" + resolved + (separator + anchor if separator else "") + ")"
        (out / name).write_text(re.sub(r"\]\(([^\s)]+)\)", published_link, source.read_text()))
    else:
        shutil.copyfile(source, out / name)
shutil.copyfile(root / "THIRD_PARTY_NOTICES.md", out / "THIRD_PARTY_NOTICES.md")
assert {path.relative_to(out).as_posix() for path in out.rglob("*") if path.is_file()} == expected
print(f"Staged GoOmni {spec['info']['version']} API reference ({len(spec['paths'])} paths)")
