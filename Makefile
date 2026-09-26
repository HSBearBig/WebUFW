.PHONY: build test
build:
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/webufw ./cmd/webufw
test:
	go test ./...
