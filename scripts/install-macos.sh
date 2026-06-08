#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFIX="${PREFIX:-/usr/local}"
PLIST_TARGET="${PLIST_TARGET:-/Library/LaunchDaemons/com.flexconnect.flexconnectd.plist}"

install -Dm755 "$ROOT/bin/flexconnectd" "$PREFIX/bin/flexconnectd"
install -Dm755 "$ROOT/bin/flexconnect" "$PREFIX/bin/flexconnect"
install -Dm755 "$ROOT/bin/flextray" "$PREFIX/bin/flextray"
install -Dm644 "$ROOT/scripts/launchd/com.flexconnect.flexconnectd.plist" "$PLIST_TARGET"

mkdir -p /usr/local/var/flexconnect
mkdir -p /usr/local/var/log

launchctl unload "$PLIST_TARGET" >/dev/null 2>&1 || true
launchctl load "$PLIST_TARGET"

echo "Installed launchd plist to $PLIST_TARGET"
