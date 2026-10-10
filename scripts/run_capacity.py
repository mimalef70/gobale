#!/usr/bin/env python3
"""Run the acceptance sequence on one frozen binary; stop on any failed gate."""
import argparse
import datetime
import json
import math
from pathlib import Path
import subprocess
import sys

from start_soak import GIB, QUEUE_LIMIT, CONNECTION_QUEUE_LIMIT, required_disk_bytes, save_record

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="goomni:dev")
    parser.add_argument("--skip-soak", action="store_true", help="Run short gates only; never report 24h acceptance")
    parser.add_argument("--max-disk-gib", type=int, default=16, help="Explicit disk cap for mixed short gates and the one-hour run; 24h uses measured growth")
    parser.add_argument("--resume", type=Path, help="Resume a disk-blocked acceptance manifest, preserving its binary, image and short-stage cap")
    args = parser.parse_args()
    if not 1 <= args.max_disk_gib <= 1024:
        parser.error("--max-disk-gib must be 1..1024")
    name = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%d-%H%M%S")
    if args.resume:
        manifest = args.resume.resolve()
        out = manifest.parent
        record = json.loads(manifest.read_text())
        prior_phases = record.get("phases", [])
        if record.get("status") != "blocked_insufficient_space" or not prior_phases or prior_phases[-1].get("status") != "blocked_insufficient_space" or any(p.get("status") != "passed" for p in prior_phases[:-1]):
            parser.error("only an acceptance sequence stopped before a disk-blocked gate can resume")
        record.setdefault("blocked_attempts", []).append(prior_phases[-1])
        record["phases"] = prior_phases[:-1]
        args.max_disk_gib = record["short_stage_disk_gib"]
        args.skip_soak = not record["soak_requested"]
        args.image = record.get("image_id", args.image)
        record["status"] = "running"
    else:
        out = ROOT / "artifacts" / "soak" / ("acceptance-" + name)
        out.mkdir(parents=True, exist_ok=False)
        record = {"status": "running", "scope": "synthetic Linux 4 CPU / 8 GiB; not live provider capacity", "short_stage_disk_gib": args.max_disk_gib, "soak_requested": not args.skip_soak,
                  "mixed_admission_limits": {"global": QUEUE_LIMIT, "connection": CONNECTION_QUEUE_LIMIT}, "phases": []}
        manifest = out / "acceptance.json"
    def save():
        save_record(manifest, record)
    frozen = None
    hour_record = None
    phases = [("native300", 300, "1s", "0s"), ("smoke", 300, "10m", "0s"), ("compare50", 50, "20m", "0s"), ("compare150", 150, "20m", "0s"), ("compare300", 300, "20m", "0s"), ("capacity", 300, "1h", "10m")]
    if not args.skip_soak:
        phases.append(("soak", 300, "24h", "10m"))
    completed = len(record["phases"])
    if args.resume:
        for index, prior in enumerate(record["phases"]):
            if index >= len(phases) or prior["phase"] != phases[index][0]:
                parser.error("acceptance phase sequence mismatch")
            evidence_path = Path(prior["run"])
            evidence = json.loads(evidence_path.read_text())
            if evidence.get("status") != "passed" or evidence.get("source_sha256") != record.get("source_sha256") or evidence.get("image_id") != record.get("image_id"):
                parser.error("previous phase evidence mismatch")
            if prior["phase"] == "smoke":
                frozen = evidence_path.parent / "capacity.test"
            if prior["phase"] == "capacity":
                hour_record = evidence_path
    # Invalid resume evidence must not overwrite the original blocked verdict.
    save()
    print(str(manifest), flush=True)
    for phase, accounts, duration, warmup in phases[completed:]:
        run_name = "goomni-soak-" + name + "-" + phase
        run_path = ROOT / "artifacts" / "soak" / run_name / "run.json"
        command = [sys.executable, str(ROOT / "scripts/start_soak.py"), "--wait", "--image", args.image, "--name", run_name,
                   "--accounts", str(accounts), "--duration", duration, "--warmup", warmup]
        if phase == "native300":
            command += ["--native", "--max-disk-gib", "1"]
        else:
            if frozen:
                command += ["--binary", str(frozen)]
            if phase != "soak":
                command += ["--max-disk-gib", str(args.max_disk_gib)]
        if phase == "soak":
            evidence = json.loads(hour_record.read_text())
            result = evidence["result"]
            gib = max(1, math.ceil(required_disk_bytes(result, 86400) / GIB))
            command += ["--disk-evidence", str(hour_record), "--max-disk-gib", str(gib)]
        entry = {"phase": phase, "run": str(run_path), "status": "running"}
        record["phases"].append(entry)
        save()
        log_path = out / (phase + ("-retry-" + name if args.resume else "") + ".log")
        with log_path.open("w") as log:
            result = subprocess.run(command, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT)
        if result.returncode != 0:
            if result.returncode == 3 and run_path.exists():
                blocked = json.loads(run_path.read_text())
                if blocked.get("status") == "blocked_insufficient_space":
                    entry["status"], record["status"] = "blocked_insufficient_space", "blocked_insufficient_space"
                    entry["reason"] = blocked.get("reason", "")
                    save()
                    raise SystemExit(f"Gate {phase} not started: insufficient disk space; evidence: {manifest}")
            entry["status"] = "failed or not started"
            record["status"] = "failed"
            save()
            raise SystemExit(f"Gate {phase} failed; inspect {log_path}")
        try:
            evidence = json.loads(run_path.read_text())
            if not isinstance(evidence, dict) or evidence.get("status") != "passed":
                raise ValueError("missing passed verdict")
            for key in ("source_sha256", "binary_sha256", "image_id", "harness"):
                if not isinstance(evidence.get(key), str) or not evidence[key]:
                    raise ValueError("missing evidence identity")
        except (OSError, ValueError) as error:
            entry["status"], record["status"] = "failed", "failed"
            save()
            raise SystemExit(f"Gate {phase} lacks valid recorded evidence: {error}")
        if record.get("source_sha256") not in (None, evidence["source_sha256"]):
            entry["status"], record["status"] = "source mismatch", "failed"
            save()
            raise SystemExit("Source changed between acceptance gates; restart the sequence")
        record["source_sha256"] = evidence["source_sha256"]
        if record.get("image_id") not in (None, evidence["image_id"]):
            entry["status"], record["status"] = "runtime image mismatch", "failed"
            save()
            raise SystemExit("Runtime image changed between acceptance gates; restart the sequence")
        record["image_id"] = evidence["image_id"]
        entry.update(binary_sha256=evidence["binary_sha256"], image_id=evidence["image_id"], harness=evidence["harness"])
        entry["status"] = evidence["status"]
        save()
        if phase != "native300" and not frozen:
            frozen = run_path.parent / "capacity.test"
        if phase == "capacity":
            hour_record = run_path
    record["status"] = "short gates passed; 24h not run" if args.skip_soak else "passed"
    save()
    print(record["status"], flush=True)


if __name__ == "__main__":
    main()
