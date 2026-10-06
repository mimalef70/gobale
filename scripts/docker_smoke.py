#!/usr/bin/env python3
"""Offline container/UI installation and restart checks using synthetic devices.

Every scenario owns a temporary container and volume. No phone authentication,
Bale RPC, provider session, real webhook, or existing runtime data is accessed.
"""
import base64
import http.cookiejar
import json
import os
import re
import secrets
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

image = sys.argv[1] if len(sys.argv) > 1 else "gobale:dev"


def scenario(base_path="", ui_enabled=True):
    name = "gobale-smoke-" + secrets.token_hex(5)
    volume = name + "-data"
    password = secrets.token_urlsafe(24)
    env = dict(os.environ, APP_BASIC_AUTH="test:" + password,
               APP_MASTER_KEY=base64.b64encode(secrets.token_bytes(32)).decode(),
               APP_BASE_PATH=base_path, APP_UI_ENABLED=str(ui_enabled).lower(),
               APP_UI_PUBLIC_ORIGIN="")
    origin = ""
    jar = http.cookiejar.CookieJar()
    browser = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    anonymous = urllib.request.build_opener()
    auth = base64.b64encode(env["APP_BASIC_AUTH"].encode()).decode()

    def docker(*args):
        return subprocess.check_output(["docker", *args], env=env, text=True,
                                       stderr=subprocess.PIPE).strip()

    def request(path, data=None, method=None, *, basic=True, session=False,
                csrf=None, instance=None, headers=None, expected=200):
        request_headers = {"Content-Type": "application/json"}
        if basic:
            request_headers["Authorization"] = "Basic " + auth
        if session or csrf is not None:
            request_headers["Origin"] = origin
        if csrf is not None:
            request_headers["X-CSRF-Token"] = csrf
        if instance is not None:
            request_headers["X-Device-Instance"] = instance
        request_headers.update(headers or {})
        raw = None if data is None else json.dumps(data).encode()
        req = urllib.request.Request(origin + base_path + path, data=raw,
                                     headers=request_headers, method=method)
        try:
            response = (browser if session else anonymous).open(req, timeout=5)
        except urllib.error.HTTPError as response_error:
            response = response_error
        with response:
            body = response.read()
            status, response_headers = response.status, response.headers
        assert status == expected, f"{req.method} {path}: expected {expected}, got {status}"
        decoded = json.loads(body) if response_headers.get_content_type() == "application/json" else body
        return decoded, response_headers

    def ready():
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            try:
                if request("/ready")[0]["code"] == "SUCCESS":
                    return
            except Exception:
                time.sleep(.2)
        raise RuntimeError("container did not become ready")

    def login():
        result, headers = request("/ui/auth/session", {"username": "test", "password": password},
                                  basic=False, session=True)
        csrf = result["results"]["csrf_token"]
        assert csrf and result["results"]["expires_at"] and result["results"]["absolute_expires_at"]
        assert "WWW-Authenticate" not in headers
        cookie = next(cookie for cookie in jar if cookie.name == "gobale_admin")
        assert cookie.path == base_path + "/ui/"
        assert cookie.has_nonstandard_attr("HttpOnly")
        assert cookie.get_nonstandard_attr("SameSite").lower() == "strict"
        return csrf, cookie.value

    try:
        docker("volume", "create", volume)
        docker("run", "-d", "--name", name, "--read-only", "--cap-drop=ALL",
               "--security-opt=no-new-privileges", "--tmpfs", "/tmp",
               "-e", "APP_BASIC_AUTH", "-e", "APP_MASTER_KEY", "-e", "APP_BASE_PATH",
               "-e", "APP_UI_ENABLED", "-e", "APP_UI_PUBLIC_ORIGIN",
               "-v", volume + ":/app/storages", "-p", "127.0.0.1::3000", image)
        port = docker("port", name, "3000/tcp").rsplit(":", 1)[1]
        origin = "http://127.0.0.1:" + port
        ready()
        created = request("/devices", {"device_id": "smoke"}, expected=201)[0]["results"]
        assert created["id"] == "smoke" and created["instance_id"]
        if ui_enabled:
            html, headers = request("/ui/", basic=False)
            assert isinstance(html, bytes) and b"<html" in html
            assert (base_path + "/ui/").encode() in html
            assert b"__GOBALE_UI_BASE__" not in html
            csp = headers["Content-Security-Policy"]
            assert "script-src 'self'" in csp and "frame-ancestors 'none'" in csp
            assert "unsafe-eval" not in csp
            assert headers["Cache-Control"] == "no-store"
            assert headers["X-Content-Type-Options"] == "nosniff"
            script = re.search(rb'<script[^>]+src="([^"]+)"', html)
            assert script, "production HTML lacks a local script asset"
            asset = urllib.parse.urljoin(origin + base_path + "/ui/", script.group(1).decode())
            assert urllib.parse.urlsplit(asset).netloc == urllib.parse.urlsplit(origin).netloc
            relative = urllib.parse.urlsplit(asset).path.removeprefix(base_path)
            js, asset_headers = request(relative, basic=False)
            assert js and "immutable" in asset_headers["Cache-Control"]
            request("/ui/.placeholder", basic=False, expected=404)
            request("/ui/build-manifest.json", basic=False, expected=404)
            request("/ui/auth/session", method="PUT", basic=False, expected=404)
            _, denied = request("/ui/api/devices", basic=True, expected=401)
            assert "WWW-Authenticate" not in denied
            csrf, cookie_value = login()
            request("/ui/auth/session", method="GET", basic=False, session=True)
            # Force the path-scoped browser cookie onto the public API: even then
            # it cannot replace administrative Basic authentication.
            _, denied = request("/devices", basic=False,
                                headers={"Cookie": "gobale_admin=" + cookie_value}, expected=401)
            assert "Basic" in denied.get("WWW-Authenticate", "")
            request("/ui/api/devices/smoke/status", basic=False, session=True, expected=400)
            request("/ui/api/devices/smoke/status", basic=False, session=True,
                    instance=created["instance_id"])
            # No provider call: the wrong immutable device precondition rejects
            # a stale form before the protected DELETE handler can execute.
            request("/ui/api/devices/smoke", method="DELETE", basic=False, session=True,
                    csrf=csrf, instance="stale-instance", expected=409)
            request("/ui/api/devices/smoke", method="DELETE", basic=False, session=True,
                    instance=created["instance_id"], expected=403)
            request("/ui/api/send/message", {}, basic=False, session=True, csrf=csrf, expected=404)
            request("/devices/smoke", method="DELETE")
            replaced = request("/devices", {"device_id": "smoke"}, expected=201)[0]["results"]
            assert replaced["instance_id"] != created["instance_id"]
            request("/ui/api/devices/smoke/status", basic=False, session=True,
                    instance=created["instance_id"], expected=409)
            created = replaced
            request("/ui/auth/session", method="DELETE", basic=False, session=True, csrf=csrf)
            request("/ui/auth/session", method="GET", basic=False, session=True, expected=401)
            _, cookie_value = login()
        else:
            request("/ui/", expected=404)
            request("/ui/auth/session", expected=404)
            request("/ui/api/devices", expected=404)

        docker("restart", name)
        # Docker may allocate a new ephemeral host port after a restart; cookie
        # scope is host/path, so the old cookie still reaches the restarted UI.
        port = docker("port", name, "3000/tcp").rsplit(":", 1)[1]
        origin = "http://127.0.0.1:" + port
        ready()
        restored = request("/devices/smoke")[0]["results"]
        assert restored == created, "device changed or disappeared after restart"
        if ui_enabled:
            request("/ui/auth/session", method="GET", basic=False, session=True,
                    headers={"Cookie": "gobale_admin=" + cookie_value}, expected=401)
            login()
        user = docker("inspect", "--format={{.Config.User}}", name)
        assert user == "65532:65532", "image must run as non-root"
        return {"base_path": base_path, "ui_enabled": ui_enabled, "ready": True,
                "restart_persistence": True, "read_only_root": True, "user": user,
                "embedded_ui_auth_and_device_scope": ui_enabled,
                "session_invalidated_on_restart": ui_enabled}
    finally:
        subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL,
                       stderr=subprocess.DEVNULL)
        subprocess.run(["docker", "volume", "rm", volume], stdout=subprocess.DEVNULL,
                       stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    results = [scenario(), scenario("/gateway"), scenario(ui_enabled=False)]
    print(json.dumps({"image": image, "offline": True, "scenarios": results}))
