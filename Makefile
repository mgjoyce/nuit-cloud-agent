VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: test build vectors clean

test:
	go vet ./...
	go test ./...

# Static binaries for the machines a Nuit Cloud Server runs on (no runtime needed on the machine).
build:
	mkdir -p dist
	for arch in amd64 arm64; do \
	  GOOS=linux GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/nuit-cloud-agent-linux-$$arch ./cmd/nuit-cloud-agent; \
	done
	cd dist && sha256sum nuit-cloud-agent-linux-* > SHA256SUMS 2>/dev/null || shasum -a 256 nuit-cloud-agent-linux-* > SHA256SUMS

clean:
	rm -rf dist
