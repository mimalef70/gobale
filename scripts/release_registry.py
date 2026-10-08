#!/usr/bin/env python3
"""Read-only registry checks for immutable, explicitly versioned publication."""
import argparse
import base64
import hashlib
import json
import os
import re
import urllib.error
import urllib.parse
import urllib.request

from release_notes import release_metadata

ACCEPT = ", ".join(("application/vnd.oci.image.index.v1+json",
                    "application/vnd.docker.distribution.manifest.list.v2+json",
                    "application/vnd.oci.image.manifest.v1+json",
                    "application/vnd.docker.distribution.manifest.v2+json"))
MAX_RESPONSE_BYTES = 2 * 1024 * 1024


class RegistryError(ValueError):
    """A registry check could not establish the required publication invariant."""


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, url):
        return None


def request(url, authorization, *, accept="application/json"):
    # Credentials only go to the fixed registry/auth hosts below. Do not forward
    # them through redirects or include registry response bodies in diagnostics.
    headers = {"Accept": accept}
    if authorization:
        headers["Authorization"] = authorization
    req = urllib.request.Request(url, headers=headers)
    try:
        try:
            response = urllib.request.build_opener(NoRedirect).open(req, timeout=30)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            body = response.read(MAX_RESPONSE_BYTES + 1)
            if len(body) > MAX_RESPONSE_BYTES:
                raise RegistryError("Registry response exceeded its size bound")
            return response.status, response.headers, body
    except (OSError, urllib.error.URLError) as error:
        raise RegistryError("Registry request failed; publication state is unverified") from None


def registry_targets(environment):
    required = ("GITHUB_REPOSITORY", "GITHUB_ACTOR", "GH_TOKEN",
                "DOCKERHUB_USERNAME", "DOCKERHUB_TOKEN")
    if any(not environment.get(name, "").strip() for name in required):
        raise RegistryError("GitHub credentials and DOCKERHUB_USERNAME/DOCKERHUB_TOKEN are required")
    repository = environment["GITHUB_REPOSITORY"].lower()
    if not re.fullmatch(r"[a-z0-9][a-z0-9_.-]*/[a-z0-9][a-z0-9_.-]*", repository):
        raise RegistryError("Invalid GitHub repository identity")
    return (
        ("ghcr.io/" + repository, "https://ghcr.io", "https://ghcr.io/token",
         "ghcr.io", repository, environment["GITHUB_ACTOR"], environment["GH_TOKEN"]),
        ("docker.io/mimalef70/gobale", "https://registry-1.docker.io",
         "https://auth.docker.io/token", "registry.docker.io", "mimalef70/gobale",
         environment["DOCKERHUB_USERNAME"], environment["DOCKERHUB_TOKEN"]),
    )


def read_manifest(target, reference):
    image, registry, auth_endpoint, service, repository, username, password = target
    basic = base64.b64encode((username + ":" + password).encode()).decode()
    query = urllib.parse.urlencode({"service": service, "scope": "repository:" + repository + ":pull"})
    status, _, raw = request(auth_endpoint + "?" + query, "Basic " + basic if username or password else "")
    if status != 200:
        raise RegistryError(image + ": registry authentication failed (HTTP " + str(status) + ")")
    try:
        payload = json.loads(raw)
        token = payload.get("token") or payload.get("access_token")
    except (ValueError, AttributeError):
        token = None
    if not isinstance(token, str) or not token or any(ord(char) < 33 or ord(char) > 126 for char in token):
        raise RegistryError(image + ": registry authentication returned no usable token")
    return request(registry + "/v2/" + repository + "/manifests/" + reference,
                   "Bearer " + token, accept=ACCEPT)


def check_absent(tag, environment):
    release_metadata(tag)
    for target in registry_targets(environment):
        status, _, raw = read_manifest(target, tag)
        if status == 200:
            raise RegistryError(target[0] + ":" + tag + " already exists; inspect partial publication before retrying")
        try:
            errors = json.loads(raw)["errors"]
            codes = {error["code"] for error in errors}
        except (ValueError, KeyError, TypeError):
            codes = set()
        if status != 404 or not codes or not codes <= {"MANIFEST_UNKNOWN", "NAME_UNKNOWN"}:
            raise RegistryError(target[0] + ": version absence is unverified (HTTP " + str(status) + ")")


def check_published(tag, digest, environment):
    release_metadata(tag)
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", digest):
        raise RegistryError("Image publication did not return a valid digest")
    for target in registry_targets(environment):
        status, headers, raw = read_manifest(target, tag)
        actual = "sha256:" + hashlib.sha256(raw).hexdigest()
        if status != 200 or actual != digest or headers.get("Docker-Content-Digest", digest) != digest:
            raise RegistryError(target[0] + ": published version does not match the built immutable digest")
        try:
            manifests = json.loads(raw)["manifests"]
            architectures = {entry["platform"]["architecture"] for entry in manifests
                             if entry.get("platform", {}).get("os") == "linux"}
        except (ValueError, KeyError, TypeError, AttributeError):
            architectures = set()
        if architectures != {"amd64", "arm64"}:
            raise RegistryError(target[0] + ": published index does not contain both supported Linux architectures")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("absent", "published"))
    parser.add_argument("--tag", required=True)
    parser.add_argument("--digest", default="")
    args = parser.parse_args()
    try:
        if args.mode == "absent":
            check_absent(args.tag, os.environ)
        else:
            check_published(args.tag, args.digest, os.environ)
    except ValueError as error:
        parser.exit(1, "Release registry check: " + str(error) + "\n")
    print("Both version tags " + ("are absent." if args.mode == "absent" else "match the built multi-platform digest."))


if __name__ == "__main__":
    main()
