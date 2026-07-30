.PHONY: build build-all clean test vet

BINARY  := naivepanel
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) ./cmd/naivepanel

# 交叉编译 linux amd64 + arm64（发布用）
build-all:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 ./cmd/naivepanel
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 ./cmd/naivepanel

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf bin/ dist/
