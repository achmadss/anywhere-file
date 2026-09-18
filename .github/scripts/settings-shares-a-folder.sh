#!/usr/bin/env bash
# #136's acceptance, with a real dufs on the machine. A folder chosen through the settings
# endpoint is reachable through the gateway without the agent being restarted, removing it
# stops it, and the endpoint refuses anyone without the token and anyone off this machine.
set -euo pipefail

dir=$(mktemp -d)
shared="$dir/Holiday Photos"
mkdir -p "$shared"
echo "hello from the LAN" >"$shared/a.txt"
export RFM_AGENT_DIR="$dir"
gateway="https://${RFM_AGENT_ADDR}"
settings="http://${RFM_AGENT_SETTINGS_ADDR}"

# No agent.json written here. The whole point is that nobody has to.
"$AGENT" run &
agent=$!
trap 'kill "$agent" 2>/dev/null || true' EXIT

wait_until() {
	for _ in $(seq 30); do
		if "$@"; then return 0; fi
		sleep 1
	done
	echo "timed out waiting for: $*"
	exit 1
}
answering() { curl -fsS --max-time 2 -H "Authorization: Bearer $token" "$settings/v1/apps" >/dev/null; }

wait_until test -s "$dir/settings.token"
token=$(cat "$dir/settings.token")
wait_until answering
echo "the settings endpoint is up"

# Loopback is not an authorization, so the token is what actually gates it.
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$settings/v1/apps")
if [ "$code" != "401" ]; then
	echo "the settings endpoint answered $code with no token, want 401"
	exit 1
fi

# And the LAN cannot reach it at all, which is what keeps anyone on the network from
# re-sharing this machine's disk.
lan=$(ip -4 -o addr show scope global | awk 'NR == 1 { split($4, a, "/"); print a[1] }')
if curl -fsS --max-time 5 "http://$lan:${RFM_AGENT_SETTINGS_ADDR##*:}/v1/apps" >/dev/null 2>&1; then
	echo "the settings endpoint answered on $lan, so the LAN can change what this PC shares"
	exit 1
fi
echo "refused without the token and refused from $lan"

# The directory is walked by the agent, because a browser cannot hand a server a real path.
curl -fsS --max-time 5 -H "Authorization: Bearer $token" \
	--get --data-urlencode "path=$dir" "$settings/v1/browse" | grep -q 'Holiday Photos'
echo "browsing finds the folder"

name=$(curl -fsS --max-time 5 -H "Authorization: Bearer $token" -X POST \
	--data "$(printf '{"path": "%s"}' "$shared")" "$settings/v1/apps" |
	sed -n 's/.*"name":"\([^"]*\)".*/\1/p')
if [ "$name" != "holiday-photos" ]; then
	echo "the agent called it '$name', want holiday-photos"
	exit 1
fi

# No restart anywhere above. The route and the application both came from the change.
wait_until curl -fsSk --max-time 2 -o "$dir/got" "$gateway/$name/a.txt"
cmp "$shared/a.txt" "$dir/got"
echo "the folder is served at /$name without the agent being restarted"

curl -fsS --max-time 5 -H "Authorization: Bearer $token" -X DELETE "$settings/v1/apps/$name" >/dev/null
code=$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 "$gateway/$name/a.txt")
if [ "$code" != "404" ]; then
	echo "the gateway answered $code for /$name after it was removed, want 404"
	exit 1
fi
# A dufs left behind would hold the port and go on serving the folder with nothing in
# front of it.
for _ in $(seq 10); do
	left=$(pgrep -f "dufs .*--path-prefix /$name" || true)
	[ -z "$left" ] && break
	sleep 1
done
if [ -n "${left:-}" ]; then
	echo "dufs is still running at $left after the folder was removed"
	exit 1
fi
echo "removing it stopped the gateway and stopped dufs"

# The same from a terminal, which is the headless path.
"$AGENT" share add "$shared" --name files
wait_until curl -fsSk --max-time 2 -o /dev/null "$gateway/files/a.txt"
"$AGENT" share list | grep -q "^files"
"$AGENT" share rm files
echo "agent share does the same over ssh"
