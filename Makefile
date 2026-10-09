APP=memoryd
DB=data/memory.db

.PHONY: run build build-mcpd test fmt vet tidy init-db reset-db health dist-linux-amd64 dist-linux-arm64 dist-linux \
        triage-seed triage-backfill triage-classify triage-process triage-export triage-review triage-install triage-uninstall wiki-publish screenshots \
        mission-links scratch-clean dist

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

# Release archives for every supported server and client platform:
# dist/bishop-memory-<version>-<os>-<arch>.tar.gz, each with bin/memoryd,
# bin/mcpd (schema embedded), scripts/, db/ and the README. Unpack anywhere
# and run scripts/install.sh (server or client); no Go toolchain needed.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
dist:
	rm -rf dist/stage && mkdir -p dist
	@set -e; for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; name=bishop-memory-$(VERSION)-$$os-$$arch; dir=dist/stage/$$name; \
	  mkdir -p $$dir/bin; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" -o $$dir/bin/memoryd ./cmd/memoryd; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" -o $$dir/bin/mcpd ./cmd/mcpd; \
	  cp -R scripts db README.md LICENSE $$dir/ 2>/dev/null || cp -R scripts db README.md $$dir/; \
	  rm -rf $$dir/scripts/__pycache__ $$dir/scripts/screenshots/node_modules; \
	  tar -C dist/stage -czf dist/$$name.tar.gz $$name; \
	  echo "dist/$$name.tar.gz"; \
	done
	rm -rf dist/stage

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

# --- Findings triage (the wiki page Developer-Triage-Agents) -----------------------------
#
# All targets talk to the running service at BISHOP_MEMORY_URL (default
# http://127.0.0.1:8787). Override per invocation: make triage-classify
# BISHOP_MEMORY_URL=http://127.0.0.1:8788

# The service URL for the triage and client targets: BISHOP_MEMORY_URL, else
# the one in ~/.config/bishop-memory/client.env (scripts/install.sh client),
# else this machine.
CLIENT_ENV_URL := $(shell sed -n 's/^BISHOP_MEMORY_URL=//p' $${BISHOP_MEMORY_CLIENT_ENV:-$$HOME/.config/bishop-memory/client.env} 2>/dev/null | tail -n 1)
TRIAGE_URL ?= $(or $(BISHOP_MEMORY_URL),$(CLIENT_ENV_URL),http://127.0.0.1:8787)

# Load db/finding-categories.json (idempotent).
triage-seed:
	scripts/triage-seed-categories.py --url $(TRIAGE_URL)

# Set findings.harness on rows mirrored before the column existed. Pass
# REGISTER="kirsch=/abs/path/.claude/memory other=/abs/path/.claude/memory"
# the first time, before any harness has reconciled against this build.
triage-backfill:
	scripts/triage-backfill-harness.py --url $(TRIAGE_URL) $(foreach r,$(REGISTER),--register $(r))

# One-off mission backfill (docs/plans/MISSION-HUD-PLAN.md §2.4): links findings and
# documents to missions, fills step timing, drops orphan documents. Dry run
# unless APPLY=1; RENAME="old=new ..." merges stray harness names. Sync
# documents first (POST /v1/documents/sync) so debriefs carry their mission.
mission-links:
	scripts/backfill-mission-links.py --db $(DB) $(foreach r,$(RENAME),--rename-harness $(r)) $(if $(APPLY),--apply) --verbose

# Clear harness workspace scratch older than DAYS (default 30) into the Trash
# and drop it from search. Runs on the machine that holds the harness checkouts. Dry run unless APPLY=1; HARNESS=<name> limits it.
DAYS ?= 30
scratch-clean:
	scripts/clean-scratch.py --url $(TRIAGE_URL) --days $(DAYS) $(if $(HARNESS),--harness $(HARNESS)) $(if $(APPLY),--apply)

# Run the classifier now (Haiku, or the opencode default). Exits 0 with
# nothing to do when every finding is classified. RECLASSIFY=<slug>
# re-classifies one category. ENGINE=claude|opencode and MODEL=<id> override
# TRIAGE_ENGINE and the model for this run only.
triage-classify: build-mcpd
	BISHOP_MEMORY_URL=$(TRIAGE_URL) scripts/triage-run.sh classify $(if $(RECLASSIFY),--reclassify $(RECLASSIFY)) $(if $(ENGINE),--engine $(ENGINE)) $(if $(MODEL),--model $(MODEL))

# Run the processor now on the next category in rotation, or CATEGORY=<slug>.
triage-process: build-mcpd
	BISHOP_MEMORY_URL=$(TRIAGE_URL) scripts/triage-run.sh process $(if $(CATEGORY),--category $(CATEGORY)) $(if $(LIMIT),--limit $(LIMIT)) $(if $(ENGINE),--engine $(ENGINE)) $(if $(MODEL),--model $(MODEL))

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

# Regenerate the review-page screenshots in docs/wiki/images/ from made-up
# demo data on a throwaway service (scripts/screenshots/run.sh). Your real
# service and database are not touched. Publish them with `make wiki-publish`.
screenshots:
	scripts/screenshots/run.sh
