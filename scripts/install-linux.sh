#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"

install -Dm755 "$ROOT/bin/flexconnectd" "/usr/sbin/flexconnectd"
install -Dm755 "$ROOT/bin/flexconnect" "/usr/bin/flexconnect"
install -Dm755 "$ROOT/bin/flextray" "/usr/bin/flextray"
install -Dm644 "$ROOT/scripts/systemd/flexconnectd.service" "$SYSTEMD_DIR/flexconnectd.service"

mkdir -p /var/lib/flexconnect
mkdir -p /var/run

systemctl daemon-reload
systemctl enable --now flexconnectd.service

echo "Installed flexconnectd service to $SYSTEMD_DIR/flexconnectd.service"
