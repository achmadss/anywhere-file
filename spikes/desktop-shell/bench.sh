#!/usr/bin/env bash
# Streams $BIG through both paths and samples the process RSS from outside while it runs.
# Usage: BIG=/path/to/5g.bin ./bench.sh [run-list]
set -u
cd "$(dirname "$0")"

BIG="${BIG:?set BIG to the large test file}"
RUN="${1:-list,http,proto}"
OUT="${OUT:-/tmp/rfm-spike-bench}"
mkdir -p "$OUT"

SPIKE_BIG_FILE="$BIG" SPIKE_DIR="$(dirname "$BIG")" SPIKE_RUN="$RUN" \
  ./target/release/shell >"$OUT/app.log" 2>&1 &
APP=$!
echo "app pid $APP, file $(ls -l "$BIG" | awk '{print $5}') bytes, run=$RUN"

echo "elapsed_s,rss_kb" >"$OUT/rss.csv"
T0=$(date +%s)
while kill -0 "$APP" 2>/dev/null; do
  RSS=$(ps -o rss= -p "$APP" | tr -d ' ')
  [ -n "$RSS" ] && echo "$(( $(date +%s) - T0 )),$RSS" >>"$OUT/rss.csv"
  grep -q "autorun-finished" "$OUT/app.log" && break
  /bin/sleep 0.25
done
kill "$APP" 2>/dev/null
wait "$APP" 2>/dev/null

echo "--- app.log ---"
cat "$OUT/app.log"
echo "--- rss min/max/final (KiB) ---"
awk -F, 'NR>1{if(min==""||$2<min)min=$2; if($2>max)max=$2; last=$2; n++}
         END{printf "samples=%d min=%d max=%d final=%d\n", n, min, max, last}' "$OUT/rss.csv"
