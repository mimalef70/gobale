#!/usr/bin/env python3
"""Offline container installation/restart check. Never accesses a Bale account."""
import base64
import json
import os
import secrets
import subprocess
import sys
import time
import urllib.request

image = sys.argv[1] if len(sys.argv) > 1 else "gobale:dev"
name = "gobale-smoke-" + secrets.token_hex(5)
volume = name + "-data"
env = dict(os.environ, APP_BASIC_AUTH="test:" + secrets.token_urlsafe(24),
           APP_MASTER_KEY=base64.b64encode(secrets.token_bytes(32)).decode())


def docker(*args):
    return subprocess.check_output(["docker", *args], env=env, text=True,
                                   stderr=subprocess.PIPE).strip()


def request(path, data=None):
    auth = base64.b64encode(env["APP_BASIC_AUTH"].encode()).decode()
    req = urllib.request.Request(base + path,
        data=None if data is None else json.dumps(data).encode(),
        headers={"Authorization": "Basic " + auth, "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=2) as response:
        return json.load(response)


def ready():
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        try:
            if request("/ready")["code"] == "SUCCESS":
                return
        except Exception:
            time.sleep(.2)
    raise RuntimeError("container did not become ready")


try:
    docker("volume", "create", volume)
    docker("run", "-d", "--name", name, "--read-only", "--cap-drop=ALL",
           "--security-opt=no-new-privileges", "--tmpfs", "/tmp",
           "-e", "APP_BASIC_AUTH", "-e", "APP_MASTER_KEY",
           "-v", volume + ":/app/storages", "-p", "127.0.0.1::3000", image)
    port = docker("port", name, "3000/tcp").rsplit(":", 1)[1]
    base = "http://127.0.0.1:" + port
    ready()
    created = request("/devices", {"device_id": "smoke"})["results"]
    assert created["id"] == "smoke"
    docker("restart", name)
    # Docker can allocate a different ephemeral host port after a restart.
    port = docker("port", name, "3000/tcp").rsplit(":", 1)[1]
    base = "http://127.0.0.1:" + port
    ready()
    restored = request("/devices/smoke")["results"]
    assert restored == created, "device changed or disappeared after restart"
    user = docker("inspect", "--format={{.Config.User}}", name)
    assert user == "65532:65532", "image must run as non-root"
    print(json.dumps({"image": image, "ready": True, "restart_persistence": True,
                      "read_only_root": True, "user": user}))
finally:
    subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL,
                   stderr=subprocess.DEVNULL)
    subprocess.run(["docker", "volume", "rm", volume], stdout=subprocess.DEVNULL,
                   stderr=subprocess.DEVNULL)
