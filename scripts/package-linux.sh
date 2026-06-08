#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TYPE="tgz"
VERSION="0.1.0"
GOARCH="${GOARCH:-amd64}"
OUTDIR="${OUTDIR:-$ROOT/dist/packages}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --type) TYPE="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --arch) GOARCH="$2"; shift 2 ;;
    --outdir) OUTDIR="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

cd "$ROOT"
go run ./cmd/mkpkg -type "$TYPE" -version "$VERSION" -goos linux -goarch "$GOARCH" -outdir "$OUTDIR"
