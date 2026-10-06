#!/usr/bin/env python3
"""Build a reviewed GoBale prerelease bundle without copying runtime data."""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import yaml

ROOT = Path(__file__).resolve().parents[1]
PLATFORMS = {"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"}
TOP_LEVEL = (
    "readme.md", "LICENCE.txt", "CHANGELOG.md",
    "SECURITY.md", "CONTRIBUTING.md", "CODE_OF_CONDUCT.md", "SUPPORT.md",
    "docker-compose.yml",
)


def sha256(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def approved_files():
    # Only version-controlled or non-ignored candidate files may enter a release.
    # Ignored local planning/research files under docs must remain private too.
    candidates = set(subprocess.check_output(
        ["git", "ls-files", "-co", "--exclude-standard", "-z"], cwd=ROOT
    ).decode().split("\0"))
    paths = [ROOT / name for name in TOP_LEVEL]
    paths.extend(ROOT / name for name in ("docs/openapi.yaml", "docs/operations.md", "docs/webhook-payload.md"))
    for directory in ("assets",):
        paths.extend(sorted(p for p in (ROOT / directory).rglob("*")
                            if p.is_file() and p.relative_to(ROOT).as_posix() in candidates))
    paths.append(ROOT / "src/examples/webhookreceiver/main.go")
    for path in paths:
        relative = path.relative_to(ROOT)
        if relative.as_posix() not in candidates:
            raise SystemExit(f"Distribution file is excluded from the source tree: {relative}")
        if not path.is_file() or path.is_symlink():
            raise SystemExit(f"Missing or symlinked distribution file: {relative}")
        if any(part.startswith(".") or part in {"artifacts", "storages", "captures", "__pycache__"} for part in relative.parts):
            raise SystemExit(f"Unapproved distribution path: {relative}")
        if path.suffix.lower() in {".db", ".key", ".pem", ".har", ".pyc", ".log"}:
            raise SystemExit(f"Private/runtime extension in distribution: {relative}")
    return paths


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, help="Version without the v prefix")
    parser.add_argument("--output", default="dist")
    parser.add_argument("--platform", action="append", choices=sorted(PLATFORMS))
    args = parser.parse_args()
    if not re.fullmatch(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?", args.version):
        parser.error("version must be a semantic version without a v prefix")
    configured = re.search(r'const AppVersion = "([^"]+)"', (ROOT / "src/config/settings.go").read_text())[1]
    spec = yaml.safe_load((ROOT / "docs/openapi.yaml").read_text())
    if configured != args.version or spec["info"]["version"] != args.version:
        raise SystemExit("Binary configuration, OpenAPI and requested versions must match")
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    files = approved_files()
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    epoch = int(os.environ.get("SOURCE_DATE_EPOCH") or subprocess.check_output(
        ["git", "show", "-s", "--format=%ct", "HEAD"], cwd=ROOT, text=True).strip())
    manifest = {"version": args.version, "release_stage": "alpha" if "-" in args.version else "release",
                "revision": revision, "build": {"cgo": False, "tags": ["purego"], "trimpath": True}, "packages": []}
    for platform in sorted(set(args.platform or PLATFORMS)):
        goos, goarch = platform.split("/")
        name = f"gobale_{args.version}_{goos}_{goarch}.tar.gz"
        archive = output / name
        with tempfile.TemporaryDirectory(prefix="gobale-release-") as temporary:
            binary = Path(temporary) / "gobale"
            environment = dict(os.environ, CGO_ENABLED="0", GOOS=goos, GOARCH=goarch,
                               GOWORK="off", GOFLAGS="")
            subprocess.run(["go", "build", "-tags", "purego", "-trimpath", "-ldflags=-s -w", "-o", str(binary), "."],
                           cwd=ROOT / "src", env=environment, check=True)
            entries = [(binary, "gobale")] + [(path, path.relative_to(ROOT).as_posix()) for path in files]
            with archive.open("wb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=epoch) as compressed:
                with tarfile.open(fileobj=compressed, mode="w", format=tarfile.PAX_FORMAT) as tar:
                    for path, relative in entries:
                        info = tar.gettarinfo(str(path), arcname=relative)
                        info.uid = info.gid = 0
                        info.uname = info.gname = ""
                        info.mtime = epoch
                        info.mode = 0o755 if relative == "gobale" else 0o644
                        with path.open("rb") as source:
                            tar.addfile(info, source)
            with tarfile.open(archive) as tar:
                members = tar.getmembers()
                if [m.name for m in members] != [relative for _, relative in entries] or not all(m.isfile() for m in members):
                    raise SystemExit("Archive inventory verification failed")
                for source, relative in entries:
                    if hashlib.sha256(tar.extractfile(relative).read()).hexdigest() != sha256(source):
                        raise SystemExit(f"Archive byte verification failed: {relative}")
            manifest["packages"].append({"file": name, "platform": platform, "bytes": archive.stat().st_size,
                                         "sha256": sha256(archive), "entries": len(entries)})
            print(f"Packaged and verified {name} ({len(entries)} files)", flush=True)
    (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    sums = [f"{item['sha256']}  {item['file']}" for item in manifest["packages"]]
    sums.append(f"{sha256(output / 'manifest.json')}  manifest.json")
    (output / "SHA256SUMS").write_text("\n".join(sums) + "\n")


if __name__ == "__main__":
    main()
