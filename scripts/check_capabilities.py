#!/usr/bin/env python3
"""Validate the dated native capability inventory against code and OpenAPI."""
import datetime
import json
import re
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[1]
INVENTORY = ROOT / "src/internal/balemeow/testdata/coverage/capabilities.json"


def validate(data, contract):
    if data["schema_version"] != 1:
        raise ValueError("Unknown capability inventory schema")
    datetime.date.fromisoformat(data["snapshot_date"])
    statuses = data["statuses"]
    seen = set()
    for row in data["operations"]:
        operation = row["operation"]
        if operation in seen or not re.fullmatch(r"[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+", operation):
            raise ValueError(f"Duplicate or invalid native operation: {operation}")
        if row["status"] not in statuses:
            raise ValueError(f"Unknown implementation status: {operation}")
        if not row.get("implementation"):
            raise ValueError(f"Missing implementation: {operation}")
        seen.add(operation)
        for location in row["implementation"]:
            path = Path(location)
            if path.is_absolute() or ".." in path.parts or not (ROOT / path).is_file():
                raise ValueError(f"Missing implementation reference: {operation}: {path}")
    routes = set()
    for row in data["route_observations"]:
        key = row["method"], row["path"]
        if key in routes:
            raise ValueError(f"Duplicate route observation: {key}")
        if row["method"].lower() not in contract["paths"].get(row["path"], {}):
            raise ValueError(f"Observation refers to an absent OpenAPI operation: {key}")
        if not row.get("verification") or not all(row["verification"]):
            raise ValueError(f"Missing observation scope: {key}")
        routes.add(key)
    surfaces = set()
    for row in data["service_surfaces"]:
        name = row["capability"]
        if name in surfaces or row["status"] not in statuses or not row["note"]:
            raise ValueError(f"Duplicate or invalid service capability: {name}")
        surfaces.add(name)
    return len(seen), len(routes), len(surfaces)


def main():
    data = json.loads(INVENTORY.read_text())
    contract = yaml.safe_load((ROOT / "docs/openapi.yaml").read_text())
    try:
        operations, routes, surfaces = validate(data, contract)
    except (KeyError, TypeError, ValueError) as error:
        raise SystemExit(str(error)) from error
    print(f"Capability inventory: {operations} native operations, {routes} route observations, {surfaces} service boundaries; references valid")


if __name__ == "__main__":
    main()
