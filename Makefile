BINARY  := bin/kyro
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

FUZZTIME ?= 1m

.PHONY: build test lint fuzz e2e release test-installer clean

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
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck -s sh deploy/install.sh scripts/release/build.sh scripts/test-installer/inside.sh scripts/test-installer/inside-systemd.sh && \
		shellcheck scripts/e2e/run.sh scripts/test-installer/run.sh; \
	else \
		echo "shellcheck is not installed; the shell scripts were not checked"; \
	fi

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

# Builds the release files into dist/. Publishes nothing.
#   make release VERSION=v0.1.0-alpha
release:
	./scripts/release/build.sh $(VERSION)

# Runs install.sh in fresh Debian, Ubuntu and Fedora containers and under
# systemd. Needs Docker and KYRO_BOOTFILES. See scripts/test-installer/README.md.
test-installer:
	./scripts/test-installer/run.sh all

clean:
	rm -rf bin dist
