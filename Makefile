APP=memoryd
DB=data/memory.db

.PHONY: run build test fmt vet tidy init-db reset-db health dist-linux-amd64 dist-linux-arm64 dist-linux

run:
	go run ./cmd/memoryd

build:
	mkdir -p bin
	go build -o bin/$(APP) ./cmd/memoryd

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
