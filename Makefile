FUZZTIME ?= 30s

# package:FuzzTarget pairs, run one at a time (go test -fuzz only takes one target).
FUZZ_TARGETS = \
	./internal/keys:FuzzRoundTrip \
	./internal/keys:FuzzOrdering \
	./internal/wal:FuzzReader \
	./internal/sstable:FuzzOpen

.PHONY: test test-long fuzz build lint images

test:
	go test -race -short ./...

test-long:
	go test -race ./... -args -long

fuzz:
	@for t in $(FUZZ_TARGETS); do \
		pkg=$${t%%:*}; fn=$${t##*:}; \
		echo "== $$pkg $$fn"; \
		go test -run='^$$' -fuzz="^$$fn$$" -fuzztime=$(FUZZTIME) $$pkg || exit 1; \
	done

build:
	go build -o bin/ ./cmd/...

lint:
	go vet ./...
	staticcheck ./...

# README images are drawn from a real session; docs/gen's test fails when they're stale.
images:
	go run ./docs/gen
