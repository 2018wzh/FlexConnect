#!/bin/sh
set -e

systemctl --no-reload disable flexconnectd.service >/dev/null 2>&1 || :
systemctl stop flexconnectd.service >/dev/null 2>&1 || :
