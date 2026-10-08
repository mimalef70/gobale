.DEFAULT_GOAL := help
.PHONY: help build test race purego vet fmt fmt-check contracts vuln fuzz check docker-smoke ui-build ui-check ui-e2e release

help: ## Show development commands.
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "%-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ui-build ## Build the embedded UI and bin/gobale.
	cd src && go build -trimpath -o ../bin/gobale .

ui-build: ## Build and verify version-coupled embedded browser assets.
	python3 scripts/build_ui.py

ui-check: ## Typecheck, lint and unit-test the administrative UI.
	cd ui && npm ci --no-fund && npm run typecheck && npm run lint && npm test

ui-e2e: ui-build ## Test browser workflows using isolated fake accounts.
	cd ui && npm run e2e

test: ## Run tests with fake clients and temporary databases.
	cd src && go test ./...

race: ## Check concurrent code with the race detector (requires CGO).
	cd src && go test -race ./...

purego: ## Test the portable SQLite build without a C compiler.
	cd src && CGO_ENABLED=0 go test -tags purego ./...

vet: ## Run Go's static checks.
	cd src && go vet ./...

fmt: ## Format Go source files.
	find src -type f -name '*.go' -print0 | xargs -0 gofmt -w

fmt-check: ## Check formatting without modifying source.
	@unformatted="$$(find src -type f -name '*.go' -print0 | xargs -0 gofmt -l)"; \
	if [ -n "$$unformatted" ]; then printf '%s\n' "$$unformatted"; exit 1; fi

contracts: ## Verify generated OpenAPI, coverage inventory, and release tooling.
	python3 scripts/generate_openapi.py --check
	python3 scripts/check_capabilities.py
	python3 -m unittest discover -s scripts -p 'test_release*.py'
	python3 -m unittest discover -s scripts -p 'test_capacity_runner.py'
	python3 -m unittest discover -s scripts -p 'test_docker_smoke.py'

vuln: ## Scan default and shipped purego builds using pinned govulncheck.
	cd src && go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
	cd src && CGO_ENABLED=0 go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 -tags=purego ./...

fuzz: ## Exercise bounded protocol, voice, and Mini App parsers.
	cd src && go test ./internal/balemeow -run='^$$' -fuzz='^FuzzGRPCWeb$$' -fuzztime=15s -parallel=2
	cd src && go test ./internal/balemeow -run='^$$' -fuzz='^FuzzServerFrames$$' -fuzztime=15s -parallel=2
	cd src && go test ./internal/balemeow -run='^$$' -fuzz='^FuzzOggOpus$$' -fuzztime=15s -parallel=2
	cd src && go test ./internal/balemeow -run='^$$' -fuzz='^FuzzOpusPacket$$' -fuzztime=15s -parallel=2
	cd src && go test ./pkg/miniapp -run='^$$' -fuzz='^FuzzParseUnverified$$' -fuzztime=15s -parallel=2

check: fmt-check test vet purego contracts ## Run the main local verification suite.

docker-smoke: ## Test a separately built gobale:dev image without a Bale account.
	python3 scripts/docker_smoke.py gobale:dev

release: ## Publish a reviewed, committed VERSION and wait for public verification.
	python3 scripts/release.py --version "$(VERSION)"
