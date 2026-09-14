#!/usr/bin/env bash
# Provision one relay VPS: firewall, config, container, smoke checks.
#
#   REGION_ENV=regions/eu-west.env MONITOR_SUBNET=10.0.0.0/16 ./provision.sh
#
# Run from relay/deploy on the Vigil server (Debian or Ubuntu, Docker
# installed or installable with apt). The filled regions/<name>.env must exist;
# the .env.example files are placeholders, not configs. Re-running is safe.
set -euo pipefail

cd "$(dirname "$0")"

REGION_ENV="${REGION_ENV:?Set REGION_ENV, e.g. REGION_ENV=regions/eu-west.env}"
MONITOR_SUBNET="${MONITOR_SUBNET:?Set MONITOR_SUBNET, e.g. MONITOR_SUBNET=10.0.0.0/16}"
INSTALL_DIR="${INSTALL_DIR:-/etc/iroh-relay}"

if [ ! -f "$REGION_ENV" ]; then
  echo "no such env file: $REGION_ENV" >&2
  exit 1
fi

# shellcheck disable=SC1090
set -a
. "./$REGION_ENV"
set +a

echo "== firewall: public gets 80 and 443 only, metrics stay private"
if command -v ufw >/dev/null 2>&1; then
  ufw allow 80/tcp
  ufw allow 443/tcp
  ufw allow 443/udp
  ufw allow from "$MONITOR_SUBNET" to any port 9090
  ufw --force enable
else
  echo "no ufw; open 80/tcp, 443/tcp, 443/udp to the Internet and" >&2
  echo "restrict 9090/tcp to $MONITOR_SUBNET in the cloud firewall instead." >&2
fi

echo "== config: render $REGION_ENV"
mkdir -p "$INSTALL_DIR"
./render-config.sh "$REGION_ENV" > "$INSTALL_DIR/relay.toml"
chmod 600 "$INSTALL_DIR/relay.toml"

echo "== container: build and start"
REGION_ENV="$REGION_ENV" docker compose -f compose.yml up -d --build

echo "== smoke: metrics answer on the private address"
# shellcheck disable=SC1090
METRICS_HOST="${METRICS_BIND_ADDR:?}"
for _ in $(seq 1 12); do
  if curl -sf "http://$METRICS_HOST/metrics" >/dev/null; then
    echo "metrics up at $METRICS_HOST"
    break
  fi
  sleep 10
done
curl -sf "http://$METRICS_HOST/metrics" >/dev/null
echo "metrics ok"

echo "== smoke: relay answers HTTPS on 443"
curl -sfk -o /dev/null "https://${RELAY_HOSTNAME:?}"
echo "relay ok at https://$RELAY_HOSTNAME"

echo "== logs ship through the compose logging driver (json-file, rotated)."
echo "Forward them, do not just keep them. Two options, pick one:"
echo "1. syslog driver to the central collector (change compose.yml logging)."
echo "2. a promtail sidecar tailing /var/lib/docker/containers."
docker compose -f compose.yml ps
