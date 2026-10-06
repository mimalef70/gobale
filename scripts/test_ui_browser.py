#!/usr/bin/env python3
"""Exercise the embedded UI against temporary Go storage and synthetic clients.

Build assets first. This runner never opens application .env files or existing
data, starts no Bale client/webhook workers, and always owns its test server.
"""
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]


def main():
    subprocess.run([sys.executable, str(ROOT / "scripts/build_ui.py"), "--check"],
                   cwd=ROOT, check=True)
    with tempfile.TemporaryDirectory(prefix="gobale-ui-e2e-") as temporary:
        directory = Path(temporary)
        binary = directory / "uitestserver"
        subprocess.run(["go", "build", "-tags=purego,uismoke", "-trimpath",
                        "-o", str(binary), "./internal/uitestserver"],
                       cwd=ROOT / "src", check=True)
        log = directory / "synthetic-server.log"
        with log.open("w") as output:
            server = subprocess.Popen([str(binary), "--port", "0", "--base-path", "/gateway"],
                                      cwd=directory, stdout=output, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 30
            url = None
            while time.monotonic() < deadline:
                text = log.read_text()
                found = re.search(r"synthetic local test server: (http://127\.0\.0\.1:\d+/gateway/ui/)", text)
                if found:
                    url = found[1]
                    break
                if server.poll() is not None:
                    raise RuntimeError("Synthetic UI server stopped before startup:\n" + text)
                time.sleep(0.1)
            if url is None:
                raise RuntimeError("Synthetic UI server did not become ready")
            print("Testing embedded UI with isolated synthetic accounts and /gateway base path", flush=True)
            environment = dict(os.environ, GOBALE_E2E_URL=url)
            result = subprocess.run(["npx", "--no-install", "playwright", "test", *sys.argv[1:]],
                                    cwd=ROOT / "ui", env=environment)
            return result.returncode
        finally:
            server.terminate()
            try:
                server.wait(timeout=10)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait()


if __name__ == "__main__":
    raise SystemExit(main())
