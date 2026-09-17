#!/usr/bin/env bash
# Two hosts on a network with no route off it find each other over mDNS, which is the last
# acceptance case of #93. A Docker internal network is that: the containers can reach each
# other and nothing else, and nothing resolves names for them.
set -euo pipefail

IMAGE=gcr.io/distroless/static-debian12
NET=rfm-mdns

CGO_ENABLED=0 go build -o /tmp/agent ./device/agent

cleanup() {
  docker rm -f rfm-advertiser >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

docker network create --internal --subnet 10.77.0.0/24 "$NET" >/dev/null

rm -rf /tmp/agent-a && mkdir -p /tmp/agent-a && chmod 700 /tmp/agent-a
cat > /tmp/agent-a/agent.json <<'JSON'
{"name":"pc-in-a-box","apps":[{"name":"copyparty","type":"http","address":"127.0.0.1:3923"}]}
JSON

docker run -d --name rfm-advertiser --network "$NET" \
  -v /tmp/agent:/agent:ro -v /tmp/agent-a:/state \
  -e RFM_AGENT_DIR=/state -e RFM_AGENT_KEYSTORE=file -e RFM_AGENT_ADDR=:7433 \
  "$IMAGE" /agent run >/dev/null

for i in $(seq 30); do
  docker logs rfm-advertiser 2>&1 | grep -q "advertising on the LAN" && break
  sleep 1
done
docker logs rfm-advertiser
docker logs rfm-advertiser 2>&1 | grep -q "advertising on the LAN" || {
  echo "the advertiser never started"; exit 1; }

# `agent discover` gives up after two seconds, so finding the record at all is finding it
# inside the two seconds the issue asks for.
out=$(docker run --rm --network "$NET" -v /tmp/agent:/agent:ro \
  -e RFM_AGENT_DIR=/state -e RFM_AGENT_KEYSTORE=file \
  "$IMAGE" /agent discover)
echo "$out"

echo "$out" | grep -q "pc-in-a-box" || { echo "the advertiser was not found"; exit 1; }
echo "$out" | grep -q "copyparty" || { echo "the TXT record did not survive"; exit 1; }
