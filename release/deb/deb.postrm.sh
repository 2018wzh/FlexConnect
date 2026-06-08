#!/bin/sh
set -e

if [ "$1" = "purge" ]; then
	deb-systemd-helper purge 'flexconnectd.service' >/dev/null || true
fi
systemctl daemon-reload >/dev/null 2>&1 || true
