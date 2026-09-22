#!/usr/bin/env sh
set -eu
mkdir -p dist
go test ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/nodescope-linux-amd64 ./cmd/nodescope
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/nodescope-linux-arm64 ./cmd/nodescope
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/nodescope-windows-amd64.exe ./cmd/nodescope
(
  cd dist
  sha256sum nodescope-linux-amd64 nodescope-linux-arm64 nodescope-windows-amd64.exe > SHA256SUMS.txt
)
printf '%s\n' "Built and checksummed Linux amd64, Linux arm64, and Windows amd64 binaries."
