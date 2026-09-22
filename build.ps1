$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force -Path dist | Out-Null

go test ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

$env:CGO_ENABLED = "0"
$targets = @(
    @{ OS = "linux";   Arch = "amd64"; Out = "dist/nodescope-linux-amd64" },
    @{ OS = "linux";   Arch = "arm64"; Out = "dist/nodescope-linux-arm64" },
    @{ OS = "windows"; Arch = "amd64"; Out = "dist/nodescope-windows-amd64.exe" }
)
foreach ($t in $targets) {
    $env:GOOS = $t.OS
    $env:GOARCH = $t.Arch
    go build -trimpath -ldflags "-s -w" -o $t.Out ./cmd/nodescope
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

$lines = foreach ($t in $targets) {
    $hash = (Get-FileHash -Algorithm SHA256 $t.Out).Hash.ToLowerInvariant()
    $name = Split-Path -Leaf $t.Out
    "$hash  $name"
}
$lines | Set-Content -Encoding ascii dist/SHA256SUMS.txt
Write-Host "Built and checksummed Linux amd64, Linux arm64, and Windows amd64 binaries."
