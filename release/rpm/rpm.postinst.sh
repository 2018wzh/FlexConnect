#!/bin/sh
set -e

systemctl daemon-reload >/dev/null 2>&1 || :
systemctl enable flexconnectd.service >/dev/null 2>&1 || :
systemctl start flexconnectd.service >/dev/null 2>&1 || :
