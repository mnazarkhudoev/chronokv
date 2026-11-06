FUZZTIME ?= 30s

# package:FuzzTarget pairs, run one at a time (go test -fuzz only takes one target).
FUZZ_TARGETS = \
	./internal/keys:FuzzRoundTrip \
	./internal/keys:FuzzOrdering \
	./internal/wal:FuzzReader

.PHONY: test test-long fuzz build lint

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
