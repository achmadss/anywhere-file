#!/usr/bin/env bash
# Render relay.toml.template with a regions/*.env file and validate the result.
#
#   hosted/relay/deploy/render-config.sh hosted/relay/deploy/regions/eu-west.env
#
# Prints the rendered config to stdout. Fails if a required variable is
# missing, still templated, or not valid TOML. Redirect to the relay host:
#   ./render-config.sh regions/eu-west.env > /etc/iroh-relay/relay.toml
set -euo pipefail

cd "$(dirname "$0")"

if [ "$#" -ne 1 ]; then
  echo "usage: render-config.sh <regions/<name>.env>" >&2
  exit 2
fi

ENV_FILE="$1"
# Accept paths from the repo root too, e.g. hosted/relay/deploy/regions/x.env.
case "$ENV_FILE" in
  hosted/relay/deploy/*) ENV_FILE="${ENV_FILE#hosted/relay/deploy/}" ;;
  deploy/*) ENV_FILE="${ENV_FILE#deploy/}" ;;
esac
TEMPLATE="relay.toml.template"
REQUIRED="RELAY_HOSTNAME RELAY_CONTACT METRICS_BIND_ADDR ACCEPT_CONN_LIMIT ACCEPT_CONN_BURST CLIENT_RX_BYTES_PER_SECOND CLIENT_RX_MAX_BURST_BYTES"

if [ ! -f "$ENV_FILE" ]; then
  echo "no such env file: $ENV_FILE" >&2
  echo "copy regions/<name>.env.example to regions/<name>.env and fill it in." >&2
  exit 1
fi

# shellcheck disable=SC1090
set -a
. "./$ENV_FILE"
set +a

missing=""
for var in $REQUIRED; do
  if [ -z "${!var:-}" ]; then
    missing="$missing $var"
  fi
done
if [ -n "$missing" ]; then
  echo "env file $ENV_FILE is missing:$missing" >&2
  exit 1
fi

rendered="$(python3 - "$TEMPLATE" "$ENV_FILE" <<'EOF'
import re
import sys

template_path, env_path = sys.argv[1], sys.argv[2]
values = {}
with open(env_path) as f:
    for line in f:
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            key, _, value = line.partition("=")
            values[key.strip()] = value.strip()

with open(template_path) as f:
    out = f.read()

def sub(match):
    key = match.group(1)
    if key not in values:
        raise SystemExit(f"template needs {key}, not set in {env_path}")
    return values[key]

out = re.sub(r"\$\{([A-Z_]+)\}", sub, out)
if re.search(r"\$\{[A-Z_]+\}", out):
    raise SystemExit("unsubstituted variable left in rendered config")
print(out, end="")
EOF
)"

# The rendered file must parse as TOML and carry the fields issue #28 needs.
python3 -c '
import sys
import tomllib

cfg = tomllib.loads(sys.argv[1])
tls = cfg.get("tls", {})
assert tls.get("cert_mode") == "LetsEncrypt", "tls.cert_mode must be LetsEncrypt"
assert tls.get("hostname"), "tls.hostname must be set"
assert tls.get("contact"), "tls.contact must be set"
assert cfg.get("enable_metrics") is True, "enable_metrics must be true"
assert cfg.get("metrics_bind_addr"), "metrics_bind_addr must be set"
rx = cfg.get("limits", {}).get("client", {}).get("rx", {})
assert rx.get("bytes_per_second"), "limits.client.rx.bytes_per_second must be set"
assert rx.get("max_burst_bytes"), "limits.client.rx.max_burst_bytes must be set"
assert cfg.get("limits", {}).get("accept_conn_limit"), "accept_conn_limit must be set"
assert cfg.get("limits", {}).get("accept_conn_burst"), "accept_conn_burst must be set"
assert "access" in cfg, "access must be set"
print("config ok", file=sys.stderr)
' "$rendered"

printf '%s' "$rendered"
