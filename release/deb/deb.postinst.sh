#!/bin/sh
set -e

if ! getent group flexconnect >/dev/null 2>&1; then
	if command -v addgroup >/dev/null 2>&1; then
		addgroup --system flexconnect >/dev/null
	else
		groupadd --system flexconnect
	fi
fi

deb-systemd-helper unmask 'flexconnectd.service' >/dev/null || true
if deb-systemd-helper --quiet was-enabled 'flexconnectd.service'; then
	deb-systemd-helper enable 'flexconnectd.service' >/dev/null || true
else
	deb-systemd-helper update-state 'flexconnectd.service' >/dev/null || true
fi
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null
	deb-systemd-invoke restart 'flexconnectd.service' >/dev/null
	systemctl is-active --quiet flexconnectd.service
	if [ -S /var/run/flexconnect.sock ]; then
		rm -f -- /var/run/flexconnect.sock
	fi
	ready=false
	for _ in $(seq 1 20); do
		if /usr/bin/flexconnect --timeout 1s status >/dev/null 2>&1; then
			ready=true
			break
		fi
		sleep 0.25
	done
	if [ "$ready" != true ]; then
		echo "flexconnectd did not become ready after installation" >&2
		systemctl status flexconnectd.service --no-pager >&2 || true
		exit 1
	fi
fi
if command -v update-desktop-database >/dev/null 2>&1; then
	update-desktop-database -q /usr/share/applications || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
	gtk-update-icon-cache -q /usr/share/icons/hicolor || true
fi
