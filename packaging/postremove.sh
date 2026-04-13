#!/bin/sh
set -e

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
fi

if [ "$1" = "purge" ]; then
    if getent passwd pg-rds-proxy >/dev/null; then
        deluser --quiet --system pg-rds-proxy || true
    fi
    if getent group pg-rds-proxy >/dev/null; then
        delgroup --quiet --system pg-rds-proxy || true
    fi
fi

exit 0
