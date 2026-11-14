BINARY  := bin/kyro
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

FUZZTIME ?= 1m

.PHONY: build test lint fuzz e2e clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/kyro

test:
	go test ./...

lint:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt: these files need formatting:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	go vet ./...

# go test accepts one fuzz target per run, so this runs them one by one.
fuzz:
	@for pkg in $$(go list ./...); do \
		for target in $$(go test -list '^Fuzz' $$pkg | grep '^Fuzz'); do \
			echo "== $$pkg $$target"; \
			go test -run '^$$' -fuzz "^$$target\$$" -fuzztime $(FUZZTIME) $$pkg || exit 1; \
		done; \
	done

# Installs Windows 11 on a virtual machine through Kyro. Needs root, KVM,
# about 22 GB of disk and KYRO_E2E_ISO. See scripts/e2e/README.md.
e2e:
	./scripts/e2e/run.sh run

clean:
	rm -rf bin dist
