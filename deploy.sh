#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$(readlink -f "$0")")"

go build ./...
go test ./...
go install .

dest="$(go env GOBIN)"
[[ -z "$dest" ]] && dest="$(go env GOPATH)/bin"
echo "installed: $dest/ask ($(git rev-parse --short HEAD))"
