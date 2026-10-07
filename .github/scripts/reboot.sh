#!/usr/bin/env bash
# #241: the reboot step of #33 and #124, which a runner cannot do to itself. The deb goes
# into an Ubuntu VM, the VM reboots, and the agent has to answer again with the device id
# it had before. IMAGE is an Ubuntu cloud image, left untouched. Needs qemu, cloud-localds
# and /dev/kvm.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
export VERSION=${VERSION:-0.0.0}
# The VM has no keychain to unlock and no server, as on the package job's runners.
export AGENT_ENV="RFM_AGENT_KEYSTORE=file RFM_AGENT_MDNS=off RFM_AGENT_TUNNEL=off"
deb=$(ARCHES=amd64 OUT="$work" "$root/packaging/linux/build.sh" | grep '\.deb$')

ssh-keygen -q -t ed25519 -N '' -f "$work/key"
cat >"$work/user-data" <<EOF
#cloud-config
users:
  - name: u
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys: [$(cat "$work/key.pub")]
EOF
echo "instance-id: reboot" >"$work/meta-data"
cloud-localds "$work/seed.img" "$work/user-data" "$work/meta-data"
qemu-img create -q -f qcow2 -b "$IMAGE" -F qcow2 "$work/disk.qcow2" 10G

# Without -no-reboot, a reboot inside the guest restarts it in the same QEMU process.
qemu-system-x86_64 -enable-kvm -cpu host -m 2048 -smp 2 -display none -daemonize \
	-pidfile "$work/qemu.pid" -serial "file:$work/console.log" \
	-drive "file=$work/disk.qcow2,if=virtio" \
	-drive "file=$work/seed.img,if=virtio,format=raw" \
	-netdev user,id=n,hostfwd=tcp:127.0.0.1:2222-:22 -device virtio-net-pci,netdev=n
ok=0
trap 'kill "$(cat "$work/qemu.pid")" 2>/dev/null || true; [ "$ok" = 1 ] || tail -50 "$work/console.log"' EXIT

opts=(-i "$work/key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=5)
vm() { ssh "${opts[@]}" -p 2222 u@127.0.0.1 "$@"; }
boot_id() { vm cat /proc/sys/kernel/random/boot_id 2>/dev/null; }
# Waits for ssh on a boot other than $1, so a reboot that has not happened yet is not
# mistaken for one that finished.
wait_boot() {
	for _ in $(seq 120); do
		if b=$(boot_id) && [ -n "$b" ] && [ "$b" != "$1" ]; then
			echo "$b"
			return
		fi
		sleep 2
	done
	echo "the VM did not come up on ssh in 4 minutes" >&2
	return 1
}
# The id the running service puts in its discovery document, once it answers at all.
answered_id() {
	local doc
	doc=$(vm ./service-up.sh) || { echo "$doc" >&2; return 1; }
	echo "$doc" | jq -r .device_id
}

echo "== boot"
first_boot=$(wait_boot none)
vm cloud-init status --wait >/dev/null || true
scp "${opts[@]}" -P 2222 "$deb" "$root/.github/scripts/service-up.sh" u@127.0.0.1:
# Lingering is what starts a user's services at boot with nobody logged in.
vm sudo loginctl enable-linger u
vm 'sudo dpkg -i ~/*.deb'
first=$(vm RFM_AGENT_KEYSTORE=file /usr/lib/anywhere-file/agent key | awk '/^device id:/ { print $3 }')
answered=$(answered_id)
echo "device id: $first, and the agent answers as $answered"
[ "$first" = "$answered" ] || { echo "the agent answers as a different device than its key says"; exit 1; }

echo "== reboot"
vm sudo systemctl reboot || true
wait_boot "$first_boot" >/dev/null
again=$(answered_id) || { echo "the agent did not start again after the reboot"; exit 1; }
if [ "$first" != "$again" ]; then
	echo "the device id is $again after a reboot, and was $first. The PC lost its identity."
	exit 1
fi
ok=1
echo "the agent came back after a reboot as the same device: $again"
