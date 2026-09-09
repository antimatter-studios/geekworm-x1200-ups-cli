BINARY  := x1200
MODULE  := github.com/antimatter-studios/geekworm-x1200-ups-cli
# Source builds deliberately stamp nothing. Go embeds the commit and the dirty flag in the binary's
# own build info, so `x1200 version` reports the commit and says "source" without any help — and
# unlike a stamped string, that cannot be stale or forgotten. Releases are the only builds that
# stamp a version, and the pipeline does that from the tag.
LDFLAGS := -s -w

# Where deploy sends the binary. Set it on the command line or in the environment:
#   make deploy HOST=pi@raspberrypi.local
HOST ?= pi@raspberrypi.local

.PHONY: all test cover vet fmt build arm64 units snapshot check-release deploy clean

all: units vet test build

test:
	go test ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

vet:
	gofmt -l . | tee /dev/stderr | (! read)
	go vet ./...

build:
	go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY) ./cmd/$(BINARY)

# The Pi 5 is arm64. Cross-compiling needs no toolchain because nothing here uses cgo.
arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 ./cmd/$(BINARY)

# For trying a build on real hardware. Installing it properly is Pulumi's job, in the
# homelab-server stack — this target is for the loop before that is worth doing.
deploy: arm64
	scp dist/$(BINARY)-linux-arm64 $(HOST):/tmp/$(BINARY)
	ssh $(HOST) 'sudo install -m 0755 /tmp/$(BINARY) /usr/local/bin/$(BINARY) && $(BINARY) version'

# Everything a release would produce, without publishing any of it. Same command CI runs.
# Regenerates the packaged unit files from internal/service. A test fails if they are stale, so this
# is the fix rather than an optional tidy-up.
units:
	go run ./cmd/gen-units -out packaging

snapshot: units
	goreleaser release --snapshot --clean --skip=publish

check-release:
	goreleaser check

clean:
	rm -rf dist coverage.out
