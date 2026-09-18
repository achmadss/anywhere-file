#!/usr/bin/env bash
# #92's acceptance, with a real dufs on the machine. A file goes up and comes back down
# through the gateway, dufs answers on loopback and nowhere else, and killing it brings it
# back.
set -euo pipefail

dir=$(mktemp -d)
root="$dir/shared"
mkdir -p "$root"
export RFM_AGENT_DIR="$dir"
gateway="http://${RFM_AGENT_ADDR}"

cat >"$dir/agent.json" <<JSON
{
  "name": "pc1",
  "apps": [
    {
      "name": "files",
      "type": "http",
      "address": "127.0.0.1:5000",
      "command": ["dufs", "$root", "--bind", "127.0.0.1", "--port", "5000",
                  "--path-prefix", "/files", "--allow-all"]
    }
  ]
}
JSON

"$AGENT" run &
agent=$!
trap 'kill "$agent" 2>/dev/null || true' EXIT

serving() { curl -fsS --max-time 2 "$gateway/files/" >/dev/null; }
wait_until() {
	for _ in $(seq 30); do
		if "$@"; then return 0; fi
		sleep 1
	done
	echo "timed out waiting for: $*"
	exit 1
}

wait_until serving
echo "the gateway serves dufs"

# Up and down again, which is what this whole thing is for.
head -c 1048576 /dev/urandom >"$dir/sent"
curl -fsS --max-time 30 -T "$dir/sent" "$gateway/files/holiday.bin"
cmp "$dir/sent" "$root/holiday.bin"
curl -fsS --max-time 30 -o "$dir/got" "$gateway/files/holiday.bin"
cmp "$dir/sent" "$dir/got"
echo "a megabyte went up and came back"

# The links dufs writes have to land back on the gateway, which they do because the prefix
# arrives with the request and dufs is the one that strips it.
# As a browser, because dufs gives curl a plain listing with relative links and gives a
# browser the one with the absolute links this is about. Written to a file and read back,
# because `grep -q` stops at the first match and `pipefail` turns the broken pipe that
# gives curl into a failed script.
curl -fsS --max-time 5 -A "Mozilla/5.0 (X11; Linux x86_64)" -o "$dir/index.html" "$gateway/files/"
asset=$(grep -o '/files/__dufs[^"]*index\.css' "$dir/index.html" | sort -u)
if [ -z "$asset" ]; then
	echo "the index dufs served does not link back through /files/:"
	head -c 2000 "$dir/index.html"
	exit 1
fi
curl -fsS --max-time 5 -o /dev/null "$gateway$asset"
echo "the index links to $asset and the gateway serves it"

# dufs answers behind the gateway. Anything that reaches it directly reaches an application
# with no authorization in front of it.
lan=$(ip -4 -o addr show scope global | awk 'NR == 1 { split($4, a, "/"); print a[1] }')
echo "this machine is $lan"
if curl -fsS --max-time 5 "http://$lan:5000/" >/dev/null 2>&1; then
	echo "dufs answered on $lan:5000, so the gateway can be walked around"
	exit 1
fi

# The acceptance case: kill it and it comes back, with nobody typing anything.
before=$(pgrep -o -f 'dufs .*--path-prefix')
kill -9 "$before"
wait_until serving
after=$(pgrep -o -f 'dufs .*--path-prefix' || true)
if [ -z "$after" ] || [ "$after" = "$before" ]; then
	echo "dufs is still pid $before, so it was never restarted"
	exit 1
fi
curl -fsS --max-time 30 -o "$dir/again" "$gateway/files/holiday.bin"
cmp "$dir/sent" "$dir/again"
echo "killed dufs at $before, the agent started $after and the file is still served"
