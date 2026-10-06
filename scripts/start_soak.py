#!/usr/bin/env python3
"""Start an isolated synthetic Linux soak; never connects to Bale or local sessions."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--duration", default="24h", help="Integer seconds/minutes/hours, at most 48h")
    parser.add_argument("--image", default="gobale:dev", help="Locally built non-root GoBale image")
    parser.add_argument("--name", default="gobale-soak-" + datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%d-%H%M%S"))
    args = parser.parse_args()
    match = re.fullmatch(r"([1-9][0-9]*)(s|m|h)", args.duration)
    if not match or int(match[1]) * {"s": 1, "m": 60, "h": 3600}[match[2]] > 48 * 3600:
        parser.error("duration must be 1s..48h using one integer and s, m or h")
    if not re.fullmatch(r"gobale-soak-[a-z0-9-]{1,64}", args.name):
        parser.error("name must begin gobale-soak- and use lowercase letters, digits or hyphens")
    root = Path(__file__).resolve().parents[1]
    if shutil.disk_usage(root).free < 10 * 1024**3:
        raise SystemExit("At least 10 GiB free host disk is required for the capped soak")
    info = json.loads(docker("image", "inspect", args.image))[0]
    arch = info.get("Architecture")
    if info.get("Os") != "linux" or arch not in ("amd64", "arm64"):
        raise SystemExit("Expected a local Linux amd64/arm64 image")
    if info["Config"].get("User") != "65532:65532":
        raise SystemExit("Expected a non-root GoBale image (65532:65532)")
    volume = args.name + "-data"
    for kind, name in (("container", args.name), ("volume", volume)):
        exists = subprocess.run(["docker", kind, "inspect", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if exists.returncode == 0:
            raise SystemExit(f"Refusing to replace existing {kind}: {name}")
    out = root / "artifacts" / "soak" / args.name
    out.mkdir(parents=True, exist_ok=False)
    binary = out / "soak.test"
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=arch)
    subprocess.run(["go", "test", "-c", "-tags", "purego", "-trimpath", "-o", str(binary), "./usecase"], cwd=root / "src", env=env, check=True)
    # A source fingerprint records the tested snapshot even in an uncommitted checkout.
    digest = hashlib.sha256()
    for path in sorted((root / "src").rglob("*")):
        if path.is_file() and path.suffix in (".go", ".mod", ".sum", ".proto"):
            digest.update(str(path.relative_to(root)).encode() + b"\0" + path.read_bytes() + b"\0")
    docker("volume", "create", volume)
    command = ["run", "-d", "--name", args.name, "--network", "none",
               "--cpus", "4", "--memory", "8g", "--memory-swap", "8g",
               "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
               "--mount", f"type=bind,src={binary},dst=/app/soak.test,readonly",
               "--mount", f"type=volume,src={volume},dst=/app/storages",
               "-e", "TMPDIR=/app/storages", "-e", f"GOBALE_SOAK_DURATION={args.duration}",
               "-e", "GOBALE_SOAK_RATE=20", "-e", "GOBALE_SOAK_BURST_RATE=100",
               "-e", "GOBALE_SOAK_BURST_DURATION=60s", "-e", "GOBALE_SOAK_MAX_DISK_BYTES=8589934592",
               "--entrypoint", "/app/soak.test", args.image,
               "-test.run", "^TestOptionalSoak$", "-test.count=1",
               "-test.timeout=" + str(int(match[1]) * {"s": 1, "m": 60, "h": 3600}[match[2]] + 3600) + "s", "-test.v"]
    container = docker(*command)
    record = {"container": container, "name": args.name, "volume": volume,
              "started_at_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "duration": args.duration, "image_id": info["Id"], "architecture": arch,
              "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
              "source_sha256": digest.hexdigest(), "cpus": 4, "memory_bytes": 8 * 1024**3,
              "max_test_disk_bytes": 8 * 1024**3, "network": "none", "synthetic_accounts": 50,
              "command": ["docker", *command], "status": "started; result not yet known"}
    (out / "run.json").write_text(json.dumps(record, indent=2) + "\n")
    print(json.dumps({"name": args.name, "record": str(out / "run.json"), "status": record["status"]}))
    print(f"Follow: docker logs -f {args.name}")
    print(f"Status: docker inspect --format='{{{{json .State}}}}' {args.name}")
    print("Keep Docker and the host awake. Do not interpret a running container as a passed test.")


if __name__ == "__main__":
    main()
