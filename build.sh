#!/usr/bin/env sh
set -eu
mkdir -p dist
go test ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/jjp-linux-amd64 ./cmd/jjp
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/jjp-linux-arm64 ./cmd/jjp
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/jjp-windows-amd64.exe ./cmd/jjp
(
  cd dist
  sha256sum jjp-linux-amd64 jjp-linux-arm64 jjp-windows-amd64.exe > SHA256SUMS.txt
)
printf '%s\n' "Built and checksummed Linux amd64, Linux arm64, and Windows amd64 binaries."
