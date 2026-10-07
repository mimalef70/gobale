# Contributing to GoBale

Focused fixes, reproducible protocol findings and clearer integration examples
are welcome. Open an issue before a substantial API or architecture change.

Develop and test against the checked-out source. GoBale 2.0 changes the machine
API contract; follow the [upgrade guide](docs/operations.md#backup-restore-and-upgrades)
when moving from 1.x. Use the matching tag's documentation when working on a
published release. The `main` branch can advance beyond that release; use a
separate local image tag for development instead of overwriting a release tag.

Use Go **1.26.6**, Python **3.11+** and a C compiler for the default SQLite build
and race tests. Node **24.12+** / npm builds the embedded UI; Node is not a runtime
dependency. Docker is needed only for container checks. From the repository:

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r scripts/requirements.txt
cd src
go mod download
go mod verify
cd ..
make build
make check
make race
make fuzz
make vuln
make ui-check
(cd ui && npx playwright install chromium firefox webkit)
make ui-e2e
```

`make help` lists individual checks. The pure-Go SQLite build does not need a C
compiler. Normal tests use synthetic identities, fake providers and temporary
databases; no Bale account or running gateway is required. `make vuln` queries
the public Go vulnerability database, without connecting to Bale accounts.

Read [AGENTS.md](AGENTS.md) for architecture, protocol invariants, documentation
generation, privacy rules, current acceptance gaps and the release procedure.
Never commit credentials, real account data, databases or raw captures. Live
tests require explicitly identified accounts and recipients under your control.

Explain the problem, resulting behavior, relevant tests and remaining limitations
in your pull request. Update the OpenAPI contract when HTTP behavior changes.
Lifecycle, parsing, retries, recovery and account-scoping changes need meaningful
failure tests. Keep unrelated refactoring separate.

Contributions are distributed under the [MIT license](LICENCE.txt).
Follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Contracts and documentation

After changing HTTP routes or schemas, regenerate and verify the public contract:

```sh
python3 scripts/generate_openapi.py
python3 scripts/check_capabilities.py
make contracts
```

Keep the native capability ledger's dates, evidence scope and unresolved limits.
A passing fake-provider test is not live Bale verification. The complete settings
reference lives in [Operations](docs/operations.md#configuration-reference);
keep README examples brief and link to it instead of duplicating the table.

`python3 scripts/build_docs.py --output site` stages the static API reference.
The Pages workflow deploys documentation from `main`, so the hosted contract can
be newer than the latest release. A push to `main` runs CI; it does not publish
release archives or container images. Release publication requires the explicit
tag and manual workflow dispatch described in [AGENTS.md](AGENTS.md#release-procedure).
The release workflow publishes one multi-architecture container build to GHCR
and Docker Hub, then verifies both indexes against the build digest. Repository
secrets `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` are required; never put their
values in source or logs. Public anonymous downloads/pulls and restart checks
are still required after publication.

## Container checks

Build a separate image from the checkout and run the synthetic restart smokes:

```sh
docker build --file docker/golang.Dockerfile --tag gobale:dev .
make docker-smoke
```

The Docker build includes the UI and uses the pure-Go SQLite variant. The smoke
script needs no Bale account and creates temporary containers and volumes; it
does not test an existing deployment. CI runs the Linux amd64 smoke and separately
cross-builds Linux arm64. Cross-building alone does not verify arm64 runtime
behavior. Capacity fixtures and their disk/resource requirements are described
in [Operations](docs/operations.md#reproducible-capacity-acceptance); they are
separate from routine `make check` and live acceptance.

## Administrative UI development

Source is in `ui/`; the Go bundle reader is `src/ui/web`. `make ui-build` runs
`npm ci` and the production build. Vite emits local assets, third-party notices
and a manifest bound to `config.AppVersion` and `docs/openapi.yaml`. Regenerate
OpenAPI **before** building the UI. `make build`, Docker and release packaging
build real assets; never check generated assets or `node_modules` into Git.

`make check`, `make race` and package tests work with only the tracked embed
placeholder. They do not require Node or claim that the placeholder is a UI.
`make ui-check` runs typecheck, lint and component tests. Install browser engines
with `cd ui && npx playwright install chromium firefox webkit`; `make ui-e2e`
uses synthetic data and a separate temporary database, never existing Bale accounts.
The build-tagged `src/internal/uitestserver` is test infrastructure, not a service mode.
For the local Vite workflow and its backend prerequisites, see
[`ui/README.md`](ui/README.md#development). On Linux, Playwright may also require
system libraries; the CI setup uses `npx playwright install --with-deps chromium firefox webkit`.

Keep UI/API paths relative to the injected base, account requests scoped by
`instance_id`, mutation retries disabled and private data out of browser storage.
English/Persian direction, keyboard focus and mobile layouts require browser checks.
