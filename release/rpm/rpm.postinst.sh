#!/bin/sh
set -e

if ! getent group flexconnect >/dev/null 2>&1; then
	groupadd --system flexconnect
fi
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null
	systemctl enable flexconnectd.service >/dev/null
	systemctl restart flexconnectd.service >/dev/null
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
