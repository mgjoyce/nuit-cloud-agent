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
	echo $(VERSION) > dist/VERSION
	cd dist && (sha256sum nuit-cloud-agent-linux-* > SHA256SUMS 2>/dev/null || shasum -a 256 nuit-cloud-agent-linux-* > SHA256SUMS)

clean:
	rm -rf dist

# The profile features ship inside the agent's release (ADR 0001: a task names a feature, never fetches a script). They are
# written in the platform repository, deploy/bootstrap; this copies the ones the platform asks for into the agent.
BOOTSTRAP_SRC ?= ../nuit-cloud/deploy/bootstrap
.PHONY: sync-bootstrap
sync-bootstrap:
	rm -rf internal/profiles/bootstrap
	mkdir -p internal/profiles/bootstrap/profiles
	cp $(BOOTSTRAP_SRC)/bootstrap.sh $(BOOTSTRAP_SRC)/features.sh internal/profiles/bootstrap/
	cp $(BOOTSTRAP_SRC)/profiles/nuit-cloud-server.sh $(BOOTSTRAP_SRC)/profiles/nuit-container-service.sh $(BOOTSTRAP_SRC)/profiles/nuit-cloud-address.sh $(BOOTSTRAP_SRC)/profiles/nuit-cloud-vlan.sh $(BOOTSTRAP_SRC)/profiles/nuit-cloud-ssh.sh $(BOOTSTRAP_SRC)/profiles/nuit-cloud-wired.sh internal/profiles/bootstrap/profiles/
