BINARY  := x1200
MODULE  := github.com/antimatter-studios/geekworm-x1200-ups-cli
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Where deploy sends the binary. Set it on the command line or in the environment:
#   make deploy HOST=pi@raspberrypi.local
HOST ?= pi@raspberrypi.local

.PHONY: all test cover vet fmt build arm64 deploy clean

all: vet test build

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
	ssh $(HOST) 'sudo install -m 0755 /tmp/$(BINARY) /usr/local/bin/$(BINARY) && $(BINARY) --version'

clean:
	rm -rf dist coverage.out
