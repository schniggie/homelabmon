VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "unknown")
LDFLAGS := -s -w \
	-X github.com/dx111ge/homelabmon/cmd.Version=$(VERSION) \
	-X github.com/dx111ge/homelabmon/cmd.Commit=$(COMMIT) \
	-X github.com/dx111ge/homelabmon/cmd.BuildDate=$(DATE)

.PHONY: build all clean

build:
	go build -ldflags "$(LDFLAGS)" -o homelabmon .

all: linux-amd64 linux-arm64 linux-arm darwin-amd64 darwin-arm64 windows-amd64 freebsd-amd64 freebsd-arm64

linux-amd64:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/homelabmon-linux-amd64 .

linux-arm64:
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/homelabmon-linux-arm64 .

linux-arm:
	GOOS=linux GOARCH=arm GOARM=7 go build -ldflags "$(LDFLAGS)" -o dist/homelabmon-linux-arm .

darwin-amd64:
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/homelabmon-darwin-amd64 .

darwin-arm64:
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/homelabmon-darwin-arm64 .

windows-amd64:
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/homelabmon-windows-amd64.exe .

freebsd-amd64:
	GOOS=freebsd GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/homelabmon-freebsd-amd64 .

freebsd-arm64:
	GOOS=freebsd GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/homelabmon-freebsd-arm64 .

clean:
	rm -rf dist/ homelabmon homelabmon.exe
