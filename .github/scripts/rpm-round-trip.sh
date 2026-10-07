#!/usr/bin/env bash
# #226: the rpm on a current Fedora. The runner is Ubuntu, so the rpm is built here and
# installed in a Fedora container that boots systemd, which is what starts a user's service.
# It installs, the service answers, an upgrade keeps the service and the account, and the
# removal signs the PC out and stops the service.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
export VERSION=${VERSION:-0.0.0}
# The same settings as the deb's round trip, for the same reasons.
export AGENT_ENV="RFM_AGENT_ADDR=$RFM_AGENT_ADDR RFM_AGENT_KEYSTORE=$RFM_AGENT_KEYSTORE RFM_AGENT_MDNS=$RFM_AGENT_MDNS RFM_AGENT_TUNNEL=$RFM_AGENT_TUNNEL"
rpm=$(ARCHES=amd64 "$root/packaging/linux/build.sh" | grep '\.rpm$')

docker build -q -t fedora-systemd - <<'DOCKERFILE' >/dev/null
FROM fedora:latest
RUN dnf install -y systemd && dnf clean all && useradd -m pc
# On the runner, PAM cannot read the new user's account inside the container, so the user's
# systemd manager fails to start. Nothing in the package goes through PAM.
RUN printf 'account required pam_permit.so\nsession optional pam_systemd.so\n' >/etc/pam.d/systemd-user
CMD ["/sbin/init"]
DOCKERFILE
docker run -d --name fedora --privileged --cgroupns=private \
	-v "$root:/src:ro" -v "$(dirname "$rpm"):/dist:ro" fedora-systemd >/dev/null
trap 'docker rm -f fedora >/dev/null' EXIT
# As on the runner, the user's systemd manager exists only once the user lingers.
uid=$(docker exec fedora id -u pc)
for _ in $(seq 30); do
	docker exec fedora loginctl enable-linger pc 2>/dev/null &&
		docker exec fedora systemctl is-active -q "user@$uid" && break
	sleep 1
done
if ! docker exec fedora systemctl is-active -q "user@$uid"; then
	docker exec fedora systemctl status "user@$uid" || true
	echo "the user's systemd manager did not start in the container"
	exit 1
fi

# dnf as root with SUDO_USER set is what `sudo dnf` hands the scripts.
as_root() { docker exec -e SUDO_USER=pc fedora "$@"; }
up() { docker exec fedora /src/.github/scripts/service-up.sh up >/dev/null; }
down() { docker exec fedora /src/.github/scripts/service-up.sh down; }
device_id() {
	docker exec -u pc -e RFM_AGENT_KEYSTORE="$RFM_AGENT_KEYSTORE" fedora \
		/usr/lib/anywhere-file/agent key | awk '/^device id:/ { print $3 }'
}
# prerm prints one of these when it signs the PC out, whether or not the server answers.
signed_out() { grep -qE 'belongs to no account|could not sign this PC out'; }

echo "== install"
as_root dnf install -y "/dist/$(basename "$rpm")"
up
first=$(device_id)
echo "device id: $first"

echo "== upgrade over itself"
out=$(as_root dnf reinstall -y "/dist/$(basename "$rpm")" 2>&1)
echo "$out"
if echo "$out" | signed_out; then
	echo "the upgrade signed the PC out. Only a removal should."
	exit 1
fi
up
if [ "$(device_id)" != "$first" ]; then
	echo "the device id changed in the upgrade"
	exit 1
fi
echo "the service came back after the upgrade, as the same device"

echo "== remove"
out=$(as_root dnf remove -y anywhere-file 2>&1)
echo "$out"
if ! echo "$out" | signed_out; then
	echo "the removal did not try to sign the PC out"
	exit 1
fi
down
if docker exec fedora test -e /usr/lib/anywhere-file/agent; then
	echo "the agent is still there after the removal"
	exit 1
fi
echo "the rpm installs, upgrades and removes on Fedora"
