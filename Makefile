APP=memoryd
DB=data/memory.db

.PHONY: run build build-mcpd test fmt vet tidy init-db reset-db health dist-linux-amd64 dist-linux-arm64 dist-linux \
        triage-seed triage-backfill triage-classify triage-process triage-export triage-review triage-install triage-uninstall wiki-publish

run:
	go run ./cmd/memoryd

build:
	mkdir -p bin
	go build -o bin/$(APP) ./cmd/memoryd

# mcpd is the MCP adapter; the harness .mcp.json and the triage runner both
# execute bin/mcpd by absolute path, so rebuild it after any change to cmd/mcpd.
build-mcpd:
	mkdir -p bin
	go build -o bin/mcpd ./cmd/mcpd

# --- Cross-compiled Linux binaries (task-20260821-02 step 6, Job 4) -------
#
# CGO_ENABLED=0 is what makes this a true cross-compile from any host
# with no C toolchain involved: the only cgo-shaped dependency in this
# module is the SQLite driver, and it is modernc.org/sqlite — a pure-Go
# implementation (verified: confirmed no cgo file, i.e. no build-tagged
# *_cgo.go / import "C", anywhere under its module path) — so disabling
# cgo drops no functionality, it only removes the (unused) option of
# linking against a C sqlite3. A static binary (no dynamic libc, no
# dynamic loader dependency at all) is also what CGO_ENABLED=0 buys on
# ELF/Linux targets, which is why these binaries need nothing installed
# on the target host beyond the kernel + systemd (see
# scripts/install-daemon-linux.sh).
dist-linux-amd64:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/$(APP)-linux-amd64 ./cmd/memoryd

dist-linux-arm64:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/$(APP)-linux-arm64 ./cmd/memoryd

dist-linux: dist-linux-amd64 dist-linux-arm64

test:
	go test ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

tidy:
	go mod tidy

init-db:
	mkdir -p data
	sqlite3 $(DB) < db/schema.sql

reset-db:
	rm -f $(DB) $(DB)-shm $(DB)-wal
	$(MAKE) init-db

health:
	curl --silent http://127.0.0.1:8787/healthz | jq

# --- Findings triage (docs/FINDINGS-TRIAGE.md) -----------------------------
#
# All targets talk to the running service at BISHOP_MEMORY_URL (default
# http://127.0.0.1:8787). Override per invocation: make triage-classify
# BISHOP_MEMORY_URL=http://127.0.0.1:8788

TRIAGE_URL ?= $(if $(BISHOP_MEMORY_URL),$(BISHOP_MEMORY_URL),http://127.0.0.1:8787)

# Load db/finding-categories.json (idempotent).
triage-seed:
	scripts/triage-seed-categories.py --url $(TRIAGE_URL)

# Set findings.harness on rows mirrored before the column existed. Pass
# REGISTER="kirsch=/abs/path/.claude/memory other=/abs/path/.claude/memory"
# the first time, before any harness has reconciled against this build.
triage-backfill:
	scripts/triage-backfill-harness.py --url $(TRIAGE_URL) $(foreach r,$(REGISTER),--register $(r))

# Run the classifier now (Haiku). Exits 0 with nothing to do when every
# finding is classified. RECLASSIFY=<slug> re-classifies one category.
triage-classify: build-mcpd
	BISHOP_MEMORY_URL=$(TRIAGE_URL) scripts/triage-run.sh classify $(if $(RECLASSIFY),--reclassify $(RECLASSIFY))

# Run the processor now on the next category in rotation, or CATEGORY=<slug>.
triage-process: build-mcpd
	BISHOP_MEMORY_URL=$(TRIAGE_URL) scripts/triage-run.sh process $(if $(CATEGORY),--category $(CATEGORY)) $(if $(LIMIT),--limit $(LIMIT))

# Write operator decisions back into every registered harness's Markdown.
triage-export:
	scripts/export-decisions.py --url $(TRIAGE_URL) --all

# Open the review page.
triage-review:
	open $(TRIAGE_URL)/triage 2>/dev/null || xdg-open $(TRIAGE_URL)/triage 2>/dev/null || echo "open $(TRIAGE_URL)/triage in a browser"

# Install / remove the nightly launchd jobs (macOS).
triage-install:
	scripts/install-triage-schedule.sh --url $(TRIAGE_URL)

triage-uninstall:
	scripts/install-triage-schedule.sh --uninstall

# --- Wiki ------------------------------------------------------------------
# Push docs/wiki/*.md to the GitHub wiki (docs/wiki/ is the source of truth;
# see scripts/publish-wiki.sh for the one-time setup step).
wiki-publish:
	scripts/publish-wiki.sh
