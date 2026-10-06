# Contributing to GoBale

Focused fixes, reproducible protocol findings and clearer integration examples
are welcome. Open an issue before a substantial API or architecture change.

Use Go **1.26.6**, Python **3.11+** and a C compiler for the default SQLite build
and race tests. Docker is needed only for container checks. From the repository:

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
