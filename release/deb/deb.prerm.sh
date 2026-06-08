#!/bin/sh
set -e

deb-systemd-invoke stop 'flexconnectd.service' >/dev/null || true
