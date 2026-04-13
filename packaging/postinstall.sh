#!/bin/sh
set -e

if ! getent group pg-rds-proxy >/dev/null; then
    addgroup --system pg-rds-proxy
fi
if ! getent passwd pg-rds-proxy >/dev/null; then
    adduser --system --ingroup pg-rds-proxy --home /nonexistent \
            --no-create-home --shell /usr/sbin/nologin pg-rds-proxy
fi

if [ -f /etc/pg-rds-proxy/pg-rds-proxy.conf ]; then
    chown root:pg-rds-proxy /etc/pg-rds-proxy/pg-rds-proxy.conf || true
    chmod 0640 /etc/pg-rds-proxy/pg-rds-proxy.conf || true
fi

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
    systemctl enable pg-rds-proxy.service || true
    echo "pg-rds-proxy installed. Edit /etc/pg-rds-proxy/pg-rds-proxy.conf, then:"
    echo "  systemctl start pg-rds-proxy"
fi

exit 0
