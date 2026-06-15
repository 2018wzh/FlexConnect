#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
DESKTOP_FILE="$ROOT/release/dist/unixpkgs/files/flexconnect.desktop"
APP_ICON="$ROOT/assets/icons/app-256.png"

install -Dm755 "$ROOT/bin/flexconnectd" "/usr/local/sbin/flexconnectd"
install -Dm755 "$ROOT/bin/flexconnect" "/usr/local/bin/flexconnect"
install -Dm755 "$ROOT/bin/flextray" "/usr/local/bin/flextray"
install -Dm644 "$ROOT/scripts/systemd/flexconnectd.service" "$SYSTEMD_DIR/flexconnectd.service"
install -Dm644 "$APP_ICON" "/usr/share/icons/hicolor/256x256/apps/flexconnect.png"
install -Dm644 "$DESKTOP_FILE" "/usr/share/applications/flexconnect.desktop"

sed -i "s|Exec=/usr/bin/flextray|Exec=/usr/local/bin/flextray|g" "/usr/share/applications/flexconnect.desktop"
sed -i "s|ExecStart=/usr/sbin/flexconnectd|ExecStart=/usr/local/sbin/flexconnectd|g" "$SYSTEMD_DIR/flexconnectd.service"

mkdir -p /var/lib/flexconnect

if command -v update-desktop-database >/dev/null 2>&1; then
	update-desktop-database -q /usr/share/applications || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
	gtk-update-icon-cache -q /usr/share/icons/hicolor || true
fi

systemctl daemon-reload
systemctl enable --now flexconnectd.service

echo "Installed flexconnectd service to $SYSTEMD_DIR/flexconnectd.service"
