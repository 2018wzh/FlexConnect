#!/bin/sh
set -e

if [ "$1" = "purge" ]; then
	deb-systemd-helper purge 'flexconnectd.service' >/dev/null || true
fi
systemctl daemon-reload >/dev/null 2>&1 || true
if command -v update-desktop-database >/dev/null 2>&1; then
	update-desktop-database -q /usr/share/applications || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
	gtk-update-icon-cache -q /usr/share/icons/hicolor || true
fi
