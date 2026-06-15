#!/bin/sh
set -e

systemctl daemon-reload >/dev/null 2>&1 || true
deb-systemd-helper unmask 'flexconnectd.service' >/dev/null || true
if deb-systemd-helper --quiet was-enabled 'flexconnectd.service'; then
	deb-systemd-helper enable 'flexconnectd.service' >/dev/null || true
else
	deb-systemd-helper update-state 'flexconnectd.service' >/dev/null || true
fi
deb-systemd-invoke restart 'flexconnectd.service' >/dev/null || true
if command -v update-desktop-database >/dev/null 2>&1; then
	update-desktop-database -q /usr/share/applications || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
	gtk-update-icon-cache -q /usr/share/icons/hicolor || true
fi
