#!/usr/bin/env bash
# Waits for the installed agent to answer on the LAN gateway, or to stop answering.
# `up` is #33's acceptance: nobody started this process, the OS did.
set -euo pipefail

# -k because the certificate is the device's own, and the key in it is what lan-tls.sh
# checks. This is a liveness check.
url="https://${RFM_AGENT_ADDR:-127.0.0.1:7433}/.well-known/anywhere-file"

case "${1:-up}" in
up)
	for _ in $(seq 30); do
		if answer=$(curl -fsSk --max-time 2 "$url"); then
			echo "$answer"
			exit 0
		fi
		sleep 2
	done
	echo "nothing answered $url in 60s, so the service did not start itself"
	exit 1
	;;
down)
	for _ in $(seq 15); do
		if ! curl -fsSk --max-time 2 "$url" >/dev/null; then
			exit 0
		fi
		sleep 2
	done
	echo "$url still answers, so the uninstall left the agent running"
	exit 1
	;;
esac
