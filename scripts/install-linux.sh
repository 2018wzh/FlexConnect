#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFIX="${PREFIX:-/usr/local}"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"

install -Dm755 "$ROOT/bin/flexconnectd" "$PREFIX/bin/flexconnectd"
install -Dm755 "$ROOT/bin/flexconnect" "$PREFIX/bin/flexconnect"
install -Dm755 "$ROOT/bin/flextray" "$PREFIX/bin/flextray"
install -Dm644 "$ROOT/scripts/systemd/flexconnectd.service" "$SYSTEMD_DIR/flexconnectd.service"

mkdir -p /var/lib/flexconnect
mkdir -p /var/run

systemctl daemon-reload
systemctl enable --now flexconnectd.service

echo "Installed flexconnectd service to $SYSTEMD_DIR/flexconnectd.service"
