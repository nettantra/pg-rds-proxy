#!/bin/sh
set -e

if [ -d /run/systemd/system ]; then
    systemctl stop pg-rds-proxy.service || true
    systemctl disable pg-rds-proxy.service || true
fi

exit 0
