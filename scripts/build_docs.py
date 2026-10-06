#!/usr/bin/env python3
"""Stage the static API reference. No application runtime or account data needed."""
from pathlib import Path
import argparse
import json
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
if any(path.name not in expected or not path.is_file() or path.is_symlink() for path in out.iterdir()):
    raise SystemExit("Staging directory contains unrelated files; choose an empty output directory")
spec = yaml.safe_load((root / "docs/openapi.yaml").read_text())
assert spec["openapi"].startswith("3.") and spec["paths"]
for name in ("index.html", "style.css", "app.js", "icon.svg"):
    shutil.copyfile(root / "docs/site" / name, out / name)
shutil.copyfile(root / "docs/openapi.yaml", out / "openapi.yaml")
# Preserve the published JSON URL without tracking a duplicate source contract.
(out / "openapi.json").write_text(json.dumps(spec, ensure_ascii=False, indent=2) + "\n")
(out / ".nojekyll").touch()
assert {path.name for path in out.iterdir()} == expected
print(f"Staged GoBale {spec['info']['version']} API reference ({len(spec['paths'])} paths)")
