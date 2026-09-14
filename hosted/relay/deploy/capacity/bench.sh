#!/usr/bin/env bash
# Capacity baseline harness. Measures real relayed throughput against one
# deployed relay and prints one JSON line per round plus a summary.
#
#   hosted/relay/deploy/capacity/bench.sh https://relay-eu-west.example.com
#
# This measures bytes that crossed the relay, not connection counts. Issue #30
# uses the sustained bytes_per_second printed here as the denominator in its
# sizing arithmetic. Run it from a client machine with a Rust toolchain:
#
#   1. From one client near the relay: ./bench.sh <relay-url> (baseline).
#   2. Repeat from more clients at once until the per-client rate stops rising.
#      The plateau is the relay's capacity on that VPS size.
#   3. Record VPS size, region, plateau, and date. Re-run after any VPS resize
#      or iroh upgrade; the number belongs to one instance, not to the fleet.
set -euo pipefail

cd "$(dirname "$0")"

if [ "$#" -lt 1 ]; then
  echo "usage: bench.sh <relay-url> [seconds=30] [streams=4] [rounds=3]" >&2
  exit 2
fi

URL="$1"
SECONDS="${2:-30}"
STREAMS="${3:-4}"
ROUNDS="${4:-3}"

for _ in $(seq 1 "$ROUNDS"); do
  cargo run --quiet --manifest-path ../../fleet/Cargo.toml --bin relay-bench -- \
    --relay-url "$URL" --seconds "$SECONDS" --streams "$STREAMS"
done | python3 -c '
import json
import sys

rates = []
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    print(line)
    rates.append(json.loads(line)["bytes_per_second"])

if rates:
    summary = {
        "rounds": len(rates),
        "min_bytes_per_second": min(rates),
        "max_bytes_per_second": max(rates),
        "median_bytes_per_second": sorted(rates)[len(rates) // 2],
    }
    print(json.dumps(summary))
'
