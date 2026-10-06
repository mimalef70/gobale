# Contributing to GoBale

Focused fixes, reproducible protocol findings and clearer integration examples
are welcome. Open an issue before a substantial API or architecture change.

Use Go **1.26.6**, Python **3.11+** and a C compiler for the default SQLite build
and race tests. Node **24.12+** / npm builds the embedded UI; Node is not a runtime
dependency. Docker is needed only for container checks. From the repository:

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r scripts/requirements.txt
cd src
go mod download
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

Read [AGENTS.md](https://github.com/mimalef70/gobale/blob/main/AGENTS.md) for architecture, protocol invariants, documentation
generation, privacy rules, current acceptance gaps and the release procedure.
Never commit credentials, real account data, databases or raw captures. Live
tests require explicitly identified accounts and recipients under your control.

Explain the problem, resulting behavior, relevant tests and remaining limitations
in your pull request. Update the OpenAPI contract when HTTP behavior changes.
Lifecycle, parsing, retries, recovery and account-scoping changes need meaningful
failure tests. Keep unrelated refactoring separate.

Contributions are distributed under the [MIT license](LICENCE.txt).
Follow the [Code of Conduct](CODE_OF_CONDUCT.md).

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
