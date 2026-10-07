#!/usr/bin/env python3
"""Run an isolated Linux mixed workload, recording its exact binary and final verdict."""
import argparse
import datetime
import hashlib
import json
import math
import os
from pathlib import Path
import re
import shutil
import subprocess

GIB = 1024**3
QUEUE_LIMIT = 1000
CONNECTION_QUEUE_LIMIT = 100


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


def seconds(raw):
    match = re.fullmatch(r"(0|[1-9][0-9]*)(s|m|h)", raw)
    if not match:
        raise argparse.ArgumentTypeError("use integer seconds/minutes/hours")
    value = int(match[1]) * {"s": 1, "m": 60, "h": 3600}[match[2]]
    if value > 48 * 3600:
        raise argparse.ArgumentTypeError("maximum duration is 48 hours")
    return value


def source_hash(root):
    digest = hashlib.sha256()
    for path in sorted((root / "src").rglob("*")):
        if path.is_file() and (path.suffix in (".go", ".mod", ".sum", ".proto", ".json") or root / "src/ui/web/dist" in path.parents):
            digest.update(str(path.relative_to(root)).encode() + b"\0" + path.read_bytes() + b"\0")
    for name in ("start_soak.py", "run_capacity.py"):
        path = root / "scripts" / name
        if path.is_file():
            digest.update(str(path.relative_to(root)).encode() + b"\0" + path.read_bytes() + b"\0")
    return digest.hexdigest()


def file_hash(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def validate_frozen(prior, fingerprint, arch, image_id, binary_digest):
    if prior.get("harness") != "mixed" or prior.get("source_sha256") != fingerprint or prior.get("architecture") != arch or prior.get("image_id") != image_id or binary_digest != prior.get("binary_sha256"):
        raise ValueError("Frozen binary/source/image/architecture mismatch; rebuild and restart the acceptance sequence")


def required_disk_bytes(result, duration):
    growth, measured = result.get("measured_disk_growth_bytes"), result.get("duration_seconds")
    if type(growth) not in (int, float) or not math.isfinite(growth) or growth < 0 or type(measured) not in (int, float) or not math.isfinite(measured) or measured < 3600:
        raise ValueError("disk evidence requires finite nonnegative growth and at least one measured hour")
    return math.ceil(growth * duration / measured * 1.5) + 2 * GIB


def validate_evidence(evidence, expected, duration, disk_budget):
    if not isinstance(evidence, dict):
        raise ValueError("disk evidence must be a run record")
    result = evidence.get("result") or {}
    if not isinstance(result, dict) or evidence.get("status") != "passed" or result.get("passed") is not True or evidence.get("harness") != "mixed":
        raise ValueError("disk evidence must be a passed run")
    for key in ("binary_sha256", "source_sha256", "image_id", "architecture", "seed", "event_rate", "send_rate", "synthetic_accounts", "queue_limit", "connection_queue_limit"):
        if evidence.get(key) != expected.get(key):
            raise ValueError("disk evidence differs in " + key)
    for report_key, record_key in (("accounts", "synthetic_accounts"), ("event_rate", "event_rate"), ("send_rate", "send_rate"), ("seed", "seed"), ("queue_limit", "queue_limit"), ("connection_queue_limit", "connection_queue_limit")):
        if result.get(report_key) != expected.get(record_key):
            raise ValueError("disk evidence workload mismatch")
    required = required_disk_bytes(result, duration)
    if disk_budget < required:
        raise ValueError(f"disk budget below measured estimate: need at least {math.ceil(required / GIB)} GiB")


def valid_result(record, state, result):
    if state.get("ExitCode") != 0 or state.get("OOMKilled", False) or not isinstance(result, dict) or result.get("passed") is not True:
        return False
    if record.get("harness") == "native":
        return str(result.get("measurement_scope", "")).startswith("local protocol fixture,") and type(result.get("accounts")) is int and result.get("accounts") == record["synthetic_accounts"] and type(result.get("updates")) is int and result.get("updates") == record["synthetic_accounts"] and result.get("connect_workers") == 4
    if not str(result.get("measurement_scope", "")).startswith("synthetic provider,"):
        return False
    expected = {"accounts": record["synthetic_accounts"], "event_rate": record["event_rate"], "send_rate": record["send_rate"], "seed": record["seed"],
                "duration_seconds": seconds(record["duration"]), "warmup_seconds": seconds(record["warmup"]),
                "queue_limit": record.get("queue_limit"), "connection_queue_limit": record.get("connection_queue_limit")}
    return all(type(result.get(key)) in (int, float) and result[key] == value for key, value in expected.items())


def save_record(path, record):
    temporary = path.with_suffix(".json.tmp")
    temporary.write_text(json.dumps(record, indent=2) + "\n")
    temporary.replace(path)


def blocked_run(root, args, reason, evidence=None):
    out = root / "artifacts" / "soak" / args.name
    path = out / "run.json"
    if path.exists():
        raise SystemExit("Refusing to replace existing run evidence")
    out.mkdir(parents=True, exist_ok=True)
    record = {"status": "blocked_insufficient_space", "name": args.name, "reason": reason,
              "harness": "native" if args.native else "mixed", "max_test_disk_bytes": args.max_disk_gib * GIB}
    record.update(evidence or {})
    save_record(path, record)
    print(json.dumps({"record": str(path), "status": record["status"], "reason": reason}), flush=True)
    raise SystemExit(3)


def collect(record_path):
    record_path = Path(record_path)
    record = json.loads(record_path.read_text())
    if record.get("status") in ("blocked_insufficient_space", "interrupted_source_revision", "interrupted_resource_profile"):
        return record
    try:
        inspected = json.loads(docker("inspect", record["container"]))[0]
    except (subprocess.CalledProcessError, ValueError, KeyError, IndexError):
        record.update(status="failed", collection_error="recorded container unavailable")
        save_record(record_path, record)
        return record
    state = inspected["State"]
    if inspected["Id"] != record["container"] or inspected["Image"] != record["image_id"] or inspected.get("RestartCount", 0) != 0 or state.get("StartedAt") != record.get("container_started_at"):
        record.update(status="failed", collection_error="container or image identity mismatch")
        save_record(record_path, record)
        return record
    if state["Running"]:
        record["status"] = "running"
        save_record(record_path, record)
        return record
    logs = subprocess.run(["docker", "logs", record["container"]], capture_output=True, text=True)
    (record_path.parent / "output.log").write_text(logs.stdout + logs.stderr)
    result_path = record_path.parent / "result.json"
    copy = subprocess.run(["docker", "cp", record["container"] + ":/app/storages/result.json", str(result_path)], capture_output=True)
    try:
        result = json.loads(result_path.read_text()) if copy.returncode == 0 else None
    except (ValueError, OSError):
        result = None
    try:
        binary_matches = file_hash(record_path.parent / "capacity.test") == record["binary_sha256"]
    except OSError:
        binary_matches = False
    record.update(status="passed" if binary_matches and valid_result(record, state, result) else "failed",
                  finished_at_utc=state.get("FinishedAt"), exit_code=state["ExitCode"],
                  oom_killed=state.get("OOMKilled", False), result=result)
    save_record(record_path, record)
    return record


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--duration", default="1h")
    parser.add_argument("--warmup", default="10m")
    parser.add_argument("--accounts", type=int, default=300)
    parser.add_argument("--rate", type=int, default=60)
    parser.add_argument("--send-rate", type=int, default=10)
    parser.add_argument("--seed", type=int, default=1)
    parser.add_argument("--max-disk-gib", type=int, default=16)
    parser.add_argument("--disk-evidence", type=Path, help="Passed one-hour run.json, required for runs of 24h or longer")
    parser.add_argument("--binary", type=Path, help="Reuse a frozen Linux REST test binary from a prior run")
    parser.add_argument("--image", default="gobale:dev")
    parser.add_argument("--name", default="gobale-soak-" + datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%d-%H%M%S"))
    parser.add_argument("--wait", action="store_true", help="Wait for exit and save the final verdict")
    parser.add_argument("--native", action="store_true", help="Run the short real-client/local-WebSocket fixture instead of mixed REST workload")
    parser.add_argument("--collect", type=Path, help="Collect final status for an existing run.json")
    args = parser.parse_args()
    if args.collect:
        collected = collect(args.collect)
        print(json.dumps(collected, indent=2))
        if collected["status"] == "failed":
            raise SystemExit(1)
        if collected["status"] == "blocked_insufficient_space":
            raise SystemExit(3)
        return
    duration, warmup = seconds(args.duration), seconds(args.warmup)
    if not 1 <= duration <= 48 * 3600 or not 1 <= args.accounts <= 1000 or not 1 <= args.rate <= 10000 or not 5 <= args.send_rate <= 1000 or not 0 <= args.seed <= 1000000 or not 1 <= args.max_disk_gib <= 1024:
        parser.error("invalid duration, account count, rates, seed or disk budget")
    if not re.fullmatch(r"gobale-soak-[a-z0-9-]{1,64}", args.name):
        parser.error("name must begin gobale-soak- and use lowercase letters, digits or hyphens")
    root = Path(__file__).resolve().parents[1]
    disk_budget = args.max_disk_gib * 1024**3
    if args.native and args.binary:
        parser.error("native transport fixture builds its own separate frozen binary")
    if not args.native and duration >= 24 * 3600 and (not args.disk_evidence or not args.binary):
        parser.error("24h runs require --binary and --disk-evidence from the same passed one-hour run sequence")
    if shutil.disk_usage(root).free < disk_budget + 2 * 1024**3:
        blocked_run(root, args, "host free disk is below budget plus 2 GiB reserve")
    info = json.loads(docker("image", "inspect", args.image))[0]
    arch = info.get("Architecture")
    if info.get("Os") != "linux" or arch not in ("amd64", "arm64") or info["Config"].get("User") != "65532:65532":
        raise SystemExit("Expected a local Linux amd64/arm64 image with user 65532:65532")
    daemon = json.loads(docker("info", "--format={{json .}}"))
    if daemon.get("NCPU", 0) < 4 or daemon.get("MemTotal", 0) < 8 * GIB:
        raise SystemExit("Docker daemon requires at least 4 CPUs and 8 GiB; run not started")
    volume = args.name + "-data"
    for kind, name in (("container", args.name), ("volume", volume)):
        exists = subprocess.run(["docker", kind, "inspect", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if exists.returncode == 0:
            raise SystemExit(f"Refusing to replace existing {kind}: {name}")
    out = root / "artifacts" / "soak" / args.name
    out.mkdir(parents=True, exist_ok=False)
    binary = out / "capacity.test"
    fingerprint = source_hash(root)
    if args.binary:
        prior_record_path = args.binary.resolve().parent / "run.json"
        prior = json.loads(prior_record_path.read_text())
        try:
            validate_frozen(prior, fingerprint, arch, info["Id"], file_hash(args.binary))
        except ValueError as error:
            raise SystemExit(str(error))
        shutil.copyfile(args.binary, binary)
        binary.chmod(0o755)
    else:
        env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=arch, GOFLAGS="", GOWORK="off")
        package = "./internal/balemeow" if args.native else "./ui/rest"
        subprocess.run(["go", "test", "-c", "-tags", "purego", "-trimpath", "-o", str(binary), package], cwd=root / "src", env=env, check=True)
    if source_hash(root) != fingerprint:
        raise SystemExit("Source changed during build/copy; run not started")
    binary_digest = file_hash(binary)
    expected = {"source_sha256": fingerprint, "binary_sha256": binary_digest, "image_id": info["Id"], "architecture": arch,
                "synthetic_accounts": args.accounts, "event_rate": args.rate, "send_rate": args.send_rate, "seed": args.seed}
    if not args.native:
        expected.update(queue_limit=QUEUE_LIMIT, connection_queue_limit=CONNECTION_QUEUE_LIMIT)
    if not args.native and duration >= 24 * 3600:
        try:
            validate_evidence(json.loads(args.disk_evidence.read_text()), expected, duration, disk_budget)
        except (ValueError, OSError) as error:
            parser.error(str(error))
    docker("volume", "create", volume)
    common = ["--network", "none", "--cpus", "4", "--memory", "8g", "--memory-swap", "8g",
               "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--log-driver=json-file", "--log-opt=max-size=10m", "--log-opt=max-file=5",
               "--mount", f"type=bind,src={binary},dst=/app/capacity.test,readonly", "--mount", f"type=volume,src={volume},dst=/app/storages",
               "-e", "TMPDIR=/app/storages", "-e", "GOMAXPROCS=4"]
    try:
        preflight = docker("run", "--rm", *common, "-e", "GOBALE_CAPACITY_PREFLIGHT=1", "-e", f"GOBALE_SOAK_MAX_DISK_BYTES={disk_budget}",
                           "--entrypoint", "/app/capacity.test", info["Id"], "-test.run=^TestCapacityResourcePreflight$", "-test.v", "-test.timeout=30s")
    except subprocess.CalledProcessError as error:
        reports = [json.loads(line.removeprefix("GOBALE_CAPACITY_PREFLIGHT_BLOCKED ")) for line in error.output.splitlines() if line.startswith("GOBALE_CAPACITY_PREFLIGHT_BLOCKED ")]
        if reports:
            blocked_run(root, args, "Docker volume free disk is below budget plus 2 GiB reserve", dict(expected, preflight=reports[0], volume=volume))
        raise
    reports = [json.loads(line.removeprefix("GOBALE_CAPACITY_PREFLIGHT ")) for line in preflight.splitlines() if line.startswith("GOBALE_CAPACITY_PREFLIGHT ")]
    if len(reports) != 1 or reports[0].get("binary_sha256") != binary_digest or reports[0].get("architecture") != arch:
        raise SystemExit("Preflight binary/architecture evidence mismatch; run not started")
    if source_hash(root) != fingerprint or file_hash(binary) != binary_digest:
        raise SystemExit("Source/binary changed during preflight; run not started")
    command = ["run", "-d", "--name", args.name, *common]
    if args.native:
        command.extend(["-e", "GOBALE_NATIVE_CAPACITY=1"])
    for key, value in {"DURATION": args.duration, "WARMUP": args.warmup, "ACCOUNTS": args.accounts, "RATE": args.rate, "SEND_RATE": args.send_rate,
                       "SEED": args.seed, "BURST_DURATION": "60s", "MAX_DISK_BYTES": disk_budget, "RESULT": "/app/storages/result.json"}.items():
        command.extend(["-e", f"GOBALE_SOAK_{key}={value}"])
    test_name = "TestOptionalNativeCapacity" if args.native else "TestOptionalCapacity"
    timeout = 900 if args.native else duration + warmup + 900
    command.extend(["--entrypoint", "/app/capacity.test", info["Id"], f"-test.run=^{test_name}$", "-test.count=1", f"-test.timeout={timeout}s", "-test.v"])
    container = docker(*command)
    container_state = json.loads(docker("inspect", "--format={{json .State}}", container))
    record = {"container": container, "name": args.name, "volume": volume, "started_at_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "container_started_at": container_state["StartedAt"],
              "duration": args.duration, "warmup": args.warmup, "image_id": info["Id"], "architecture": arch, "harness": "native" if args.native else "mixed",
              "image_role": "isolated runtime base only; entrypoint is the recorded test binary",
              "commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip(),
              "binary_sha256": binary_digest, "source_sha256": fingerprint, "preflight": reports[0],
              "cpus": 4, "memory_bytes": 8 * 1024**3, "max_test_disk_bytes": disk_budget, "network": "none", "synthetic_accounts": args.accounts,
              "event_rate": args.rate, "send_rate": args.send_rate, "seed": args.seed, "command": ["docker", *command], "status": "running"}
    if not args.native:
        record.update(queue_limit=QUEUE_LIMIT, connection_queue_limit=CONNECTION_QUEUE_LIMIT)
    record_path = out / "run.json"
    save_record(record_path, record)
    print(json.dumps({"name": args.name, "record": str(record_path), "status": "running"}), flush=True)
    if args.wait:
        docker("wait", container)
        result = collect(record_path)
        print(json.dumps({"record": str(record_path), "status": result["status"]}), flush=True)
        if result["status"] != "passed":
            raise SystemExit(1)


if __name__ == "__main__":
    main()
