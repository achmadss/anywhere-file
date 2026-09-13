# Spike #3: DNS-SD coverage without Bonjour or Avahi, and the unplugged-router test

## The question

r3 §13.1 says mDNS "via the platform responder where present (Bonjour, Avahi) or an embedded
responder otherwise". §11.1 says every agent advertises `_rfm._tcp` with TXT `ws`, `dev`, `v`,
one record per workspace, and that iroh's own address lookup is off. So the responder is ours
on every platform, and the questions are: which Rust crate, does it work on a Windows box with
no Bonjour and a Linux box with no Avahi, is discovery under 2 s, does the one-record-per-
workspace rule survive a real responder, and what does the installer have to do about
firewalls.

## Evidence levels

Every claim below is tagged. This machine is a MacBook on macOS 15.7.3 arm64 and there is no
Windows or Linux hardware in reach, so most of the Windows answer is a source reading and it
is labelled as such.

- OBSERVED: run here, output pasted.
- CONTAINER: run in a Linux container under OrbStack. Containers share one Linux VM kernel
  (`7.0.14-orbstack`, aarch64), have no desktop session, no NetworkManager, no systemd, and
  their "LAN" is a Linux bridge in that VM. This proves library and protocol behaviour and
  proves nothing about a desktop distro's firewall, its Avahi packaging or real Wi-Fi.
- READ: from crate source at a file and line, or a cited document.
- EXPECTED: reasoning on top of the above, and could be wrong.

## The crate

`mdns-sd` 0.21.3. It is a responder and a querier in one, written in Rust, with no FFI to
`dns_sd.h` or Avahi. READ: its whole dependency set is `fastrand`, `flume`, `if-addrs`, `log`,
`mio`, `socket-pktinfo`, `socket2` (its `Cargo.toml`), and `grep -rn 'extern "C"|#\[link\]|
dns_sd|avahi|DNSService'` over `src/` matches nothing but doc comments. It binds UDP 5353
itself in `new_socket` at `service_daemon.rs:917-943`, setting `SO_REUSEADDR` unconditionally
and `SO_REUSEPORT` under `#[cfg(unix)]`. Nothing in it asks the platform for a responder, so
Bonjour and Avahi are irrelevant to whether it works.

The alternatives were `zeroconf` and `astro-dnssd`, which are bindings to Bonjour and Avahi
and therefore fail the Windows-without-Bonjour case by construction, and `libmdns`, which is a
responder with no querier.

For contrast: iroh's mDNS lives in a separate crate, `iroh-mdns-address-lookup` 0.5.0, whose
crates.io keywords include `swarm-discovery`, and `iroh/src/address_lookup.rs:46-51` points at
it. It publishes iroh's endpoint records, not `ws`/`dev`/`v`, which is the reason §11.1 has us
running our own responder even before #5 turns iroh's lookup off.

## Per-OS matrix

| OS | Responder present? | Registers | Browses | TXT intact | Two workspaces stay separate | Evidence |
|---|---|---|---|---|---|---|
| macOS 15.7.3 arm64 | mDNSResponder running | yes | yes | yes | yes | OBSERVED |
| macOS, interop with Bonjour | `dns-sd -B` / `-L` | yes | yes | yes | yes | OBSERVED |
| Linux, no Avahi (debian bookworm) | none | yes | yes | yes | yes | CONTAINER |
| Linux, Avahi 0.8 running | avahi-daemon on 5353 | yes | yes | yes | yes | CONTAINER |
| Windows 10/11, no Bonjour | Windows DNSCache does mDNS | expected yes | expected yes | expected yes | expected yes | READ + cross-compile |
| Windows 10/11, Bonjour installed | mDNSResponder.exe | expected yes | expected yes | expected yes | expected yes | EXPECTED |

### macOS (OBSERVED)

Advertiser and browser as separate processes, browser started fresh each run so nothing is
served from a warm cache. Thirty runs, each waiting for both workspace records:

```
failures: 0
samples (ms, time to resolve BOTH workspace records, sorted):
1 1 1 1 1 2 2 3 4 7 9 9 10 15 15 19 22 25 32 47 50 50 1026 1026 1038 1039 1049 1053 1062 1064
n=30 min=1 p50=19 p90=1049 max=1064 mean=289.4
```

Two clusters, and the reason is in the source. READ, `service_daemon.rs:3890-3903`: the first
query goes out after a random 10 to 50 ms jitter (RFC 6762 §5.2), and retransmissions double
from 1 s. Twenty-two of thirty runs were answered by the first query, eight by the 1 s retry.
Nothing landed between 50 ms and 1 s because there is nothing scheduled there. The 2 s target
therefore survives one lost query and fails on two, since the third attempt is at roughly 3 s.

Interop with Bonjour, both directions. Our records seen by `dns-sd`:

```
18:37:04.039  Add        3   1 local.               _rfm._tcp.           devA-wsalpha
18:37:04.039  Add        3   1 local.               _rfm._tcp.           devA-wsbeta
18:37:10.351  devA-wsbeta._rfm._tcp.local. can be reached at rfm-devA.local.:4433 (interface 1) Flags: 1
 dev=devA ws=wsbeta v=7
```

A record registered by Bonjour (`dns-sd -R`) seen by us:

```
RESOLVED ms=1169 name=bonjour-devB-wsalpha._rfm._tcp.local. ws=wsalpha dev=devB v=9 port=4433
```

### Two workspaces on one device (OBSERVED, and CONTAINER)

This is the §11.1 rule most likely to break quietly, so it got its own check. One process
registers `devA-wsalpha` and `devA-wsbeta` under the same hostname `rfm-devA.local.`. Both
survive registration, both resolve, each carries its own `ws` and the shared `dev` and `v`.
Twelve out of twelve browse runs on macOS returned both, thirty out of thirty in the timing
run above, ten out of ten between two containers. `avahi-browse` sees both too:

```
=;eth0;IPv4;devA-wsbeta;_rfm._tcp;local;rfm-devA.local;10.77.0.2;4433;"v=7" "dev=devA" "ws=wsbeta"
=;eth0;IPv4;devA-wsalpha;_rfm._tcp;local;rfm-devA.local;10.77.0.2;4433;"dev=devA" "ws=wsalpha" "v=7"
```

Nothing collapses them because DNS-SD keys on the instance name, and one instance per
(device, workspace) pair gives distinct names. Sharing a hostname across the instances is
fine and is what lets the address records be published once.

### The instance name has a hard 63-byte limit and blows up silently (OBSERVED)

A realistic instance name is a device key plus a workspace id. With a 64-hex device key and a
UUID workspace id that is 100 bytes, and this happens:

```
instance name would be 63+1+36 = 100 bytes
registered 4f3a...6778-550e8400-e29b-41d4-a716-446655440000._rfm._tcp.local. ws=... dev=... v=7
TIMEOUT after 6002 ms, resolved 0/1
```

`register()` returns `Ok`. The daemon logs nothing. No querier ever sees the service. Binary
search on the length:

```
instance-name bytes=62  -> DISCOVERED
instance-name bytes=63  -> DISCOVERED
instance-name bytes=64  -> NOT DISCOVERED
instance-name bytes=65  -> NOT DISCOVERED
```

READ, and the reason is `dns_parser.rs:1602` returning `WriteError::NameTooLong` for a label
over `MAX_LABEL_BYTES = 63`, which `dns_parser.rs:1700` then discards with the comment "The
item can never be encoded: skip it." The record is dropped from the packet and the caller is
never told.

What the agent has to do: cap the instance name at 63 bytes before calling `register`, and
treat a longer one as a bug rather than trimming at the point of use. A truncated device key
prefix plus the workspace id fits, and the full `dev` value goes in TXT where the limit is 255
bytes per property. The check belongs in `rfm-core` next to wherever the advertised name is
built, with a test at 63 and 64 bytes.

### Linux without Avahi (CONTAINER)

Two `debian:bookworm-slim` containers with no avahi package, on a Docker network created with
`--internal` and both containers given `--dns 127.0.0.1` so any unicast DNS attempt fails.
What that network actually looks like from inside:

```
10.77.0.0/24 dev eth0 proto kernel scope link src 10.77.0.2
--- ping 1.1.1.1 ---
ping: connect: Network is unreachable
--- getent hosts crates.io ---
getent exit=2
```

No default route, nothing routable off the segment, no working resolver. Ten cold browse
processes in a second container against the advertiser in the first, all ten found both
workspace records:

```
RESOLVED ms=74 name=devA-wsbeta._rfm._tcp.local. ws=wsbeta dev=devA v=7 port=4433 addrs=10.77.0.2
RESOLVED ms=1070 ...
(runs 2 through 10 all in the 1049 to 1097 ms band)
```

Nine of the ten needed the 1 s retry here, against eight of thirty on macOS. The likely cause
is the Linux bridge learning IGMP membership after the container's join, so the very first
multicast query is dropped. Worth remembering because a managed switch doing IGMP snooping
with no querier on the segment can behave the same way, and it eats half the 2 s budget.

This is the closest thing to the unplugged-router test that this machine can produce. It
proves discovery needs no Internet, no gateway and no DNS server. It does not prove anything
about a real switch, about Wi-Fi, about multicast across two physical NICs, or about two
machines that do not share a kernel, since these are two network namespaces in one Linux VM.
Doing it properly needs two machines and a dumb switch with the uplink unplugged, which is
half an hour of work for anyone who has the hardware.

### Linux with Avahi (CONTAINER)

`avahi-daemon` 0.8 started first, our responder started second in the same container. Both
hold 5353:

```
UNCONN 0 0    0.0.0.0:5353  0.0.0.0:*  users:(("rfm-mdns-spike",pid=32,fd=6),("rfm-mdns-spike",pid=32,fd=5))
UNCONN 0 0    0.0.0.0:5353  0.0.0.0:*  users:(("avahi-daemon",pid=17,fd=11))
UNCONN 0 0       [::]:5353     [::]:*  users:(("avahi-daemon",pid=17,fd=12))
UNCONN 0 0          *:5353        *:*  users:(("rfm-mdns-spike",pid=32,fd=8),("rfm-mdns-spike",pid=32,fd=7))
```

`avahi-browse -rpt _rfm._tcp` resolved both of our records with TXT intact, and our own
browser resolved both in 61 ms alongside the running daemon. Avahi being present neither helps
nor hurts, which is the answer that makes the platform matrix small.

### Windows 10/11 with no Bonjour (READ, plus a cross-compile)

Nobody ran Windows. Here is everything short of that.

The crate does not need Bonjour, per the dependency and FFI reading above. Its Windows
dependency tree resolves to `windows-sys` in place of `libc` (`cargo tree --target
x86_64-pc-windows-msvc`), and the whole thing type-checks for that target:

```
$ cargo check --release --target x86_64-pc-windows-msvc
    Checking mdns-sd v0.21.3
    Checking rfm-mdns-spike v0.0.0
    Finished `release` profile
```

That is a compile, not a run. It says the Windows code paths exist and are type-correct. It
says nothing about whether a packet leaves the box.

The one Windows-specific thing the check did catch is worth writing down: `socket2` has no
`set_reuse_port` on Windows, and Winsock has no `SO_REUSEPORT`. The first cross-compile of the
spike failed on exactly that:

```
error[E0599]: no method named `set_reuse_port` found for struct `Socket` in the current scope
```

`mdns-sd` guards the same call with `#[cfg(unix)]` at `service_daemon.rs:927`, so on Windows
it relies on `SO_REUSEADDR` alone. That matters because Windows 10 and 11 already run mDNS in
the DNSCache service, which holds UDP 5353 (Microsoft's ["mDNS in the
Enterprise"](https://techcommunity.microsoft.com/blog/networkingblog/mdns-in-the-enterprise/3275777),
which also documents the `HKLM\System\CurrentControlSet\Services\DNScache\Parameters\EnableMDNS`
switch and states that several processes can listen on 5353 at once). EXPECTED: our bind
succeeds alongside it because the Windows resolver does not take `SO_EXCLUSIVEADDRUSE`. This
is the single assumption most worth testing on a real Windows box, because if it is wrong,
`ServiceDaemon::new()` fails outright and nothing else matters.

Bonjour being installed changes nothing, since it is a third listener on the same port.

## Firewall prompts the installer has to handle, for #33

### Windows

Windows Defender Firewall blocks unsolicited inbound by default: "By default, Windows Firewall
with Advanced Security blocks all unsolicited inbound network traffic, and allows all outbound
network traffic" ([Windows Firewall Is Blocking a
Program](https://learn.microsoft.com/en-us/previous-versions/windows/it-pro/windows-server-2008-R2-and-2008/cc766312(v=ws.10))).
Inbound mDNS responses and queries from other hosts are exactly that.

The interactive "Windows Security Alert: Windows Defender Firewall has blocked some features
of this app" dialog only reaches a user who has a desktop session. r3 §13.1 installs the agent
as a user-level service, and a service in session 0 cannot show it, so the traffic is dropped
with nothing on screen. EXPECTED, from the session-0 isolation rule plus the default-block
rule above.

So the installer creates the rules itself, at install time, elevated. Tauri's Windows bundler
is WiX, and WiX ships `WixFirewallExtension` with a `FirewallException` element that takes a
port and protocol or a program path, so this is markup in the installer rather than a custom
action. Two inbound rules:

- UDP 5353 inbound, program-scoped to the agent executable, profiles Domain and Private.
- UDP and TCP on the agent's own data port, same scoping, for iroh.

Leave the Public profile alone. A device on a coffee shop network should not be answering
`_rfm._tcp` queries, and #33 can offer it as a setting later if anyone asks.

On uninstall the rules go with the component, which `FirewallException` handles as long as it
is a child of the component that carries the executable.

### macOS

Two separate mechanisms, and only one of them is the old firewall.

The application firewall ("Do you want the application to accept incoming network
connections?") is off on this machine, OBSERVED:

```
$ /usr/libexec/ApplicationFirewall/socketfilterfw --getglobalstate
Firewall is disabled. (State = 0)
```

So the prompt could not be triggered here and was not. Turning the firewall on needs an admin
password, which is a change to the user's machine that this spike did not make. To actually
see the dialog and find out whether it recurs per build: enable the application firewall, then
launch a signed `.app` bundle that binds 5353, then rebuild and relaunch. The relevant setting
for the installer is `socketfilterfw --add <path>` plus `--unblockapp`, which needs root, so it
belongs in the installer's postinstall script rather than in the app.

Local Network privacy is the one that actually bites on macOS 15. It is enforced through the
Network Extension framework rather than TCC, which matches what is on disk here: OBSERVED, the
user TCC database has no local-network service, and a query for `service like '%etwork%'` over
`/Library/Application Support/com.apple.TCC/TCC.db` returns nothing. Consent is keyed partly on
the executable's Mach-O UUID (["Last Week on My Mac: Local network privacy
revealed"](https://eclecticlight.co/2026/01/18/last-week-on-my-mac-local-network-privacy-revealed/)),
and the unified log shows the system tracking our unsigned binary by exactly that:

```
symptomsd: Can't lookup UUID A7F1B775-1D71-375E-881C-2EC348605D78 for procname rfm-mdns-spike
symptomsd: Can't lookup UUID DAAE6721-E5BB-3E82-92A8-DDC98D34D152 for procname rfm-mdns-spike
```

Two UUIDs, same path, two builds. OBSERVED: `dwarfdump --uuid` confirms the UUID is derived
from the emitted binary, unchanged by a no-op rebuild and changed by any real code change. So
during development the prompt is likely to reappear on every build that changes code, and in
production it reappears whenever a shipped update changes the executable. EXPECTED, from the
UUID keying rather than from a prompt anyone watched.

No prompt appeared during this spike and multicast worked throughout, because a bare CLI
binary's responsible process is the terminal that launched it, and that already had access. A
`.app` is its own responsible process and gets its own prompt. The installer cannot pre-grant
this: there is no configuration profile payload for it and no way to reset it back to
undetermined either. What the app can do is ship `NSLocalNetworkUsageDescription` in
`Info.plist` so the dialog explains itself, and detect the denied state by discovering nothing
at all, since a denied send fails silently.

For reference, the raw socket behaviour on a Mac with mDNSResponder already running, OBSERVED:

```
plain bind 0.0.0.0:5353 FAILED: Address already in use (os error 48) (kind AddrInUse)
reuse bind 0.0.0.0:5353 ok
join 224.0.0.251 ok
sent 33 bytes to 224.0.0.251:5353
received 33 bytes from 192.168.2.141:5353
```

A plain `UdpSocket::bind` is not usable. `SO_REUSEADDR` and `SO_REUSEPORT` have to be set
before the bind, which is what `mdns-sd` does.

### Linux

No prompt exists to handle: there is no per-application firewall UI, and both `ufw` on Ubuntu
and `firewalld` on Fedora default to allowing outbound and dropping unsolicited inbound.
EXPECTED, since the container has no firewall at all and this was not observed on a desktop
distro. The packaging work is a `firewalld` service file allowing UDP 5353 in the default
zone, and a note in the docs for `ufw allow 5353/udp`, both applied by the package's
post-install where the tool is present.

## What it means for the plan

`mdns-sd` 0.21.3 is the crate. It removes the "platform responder where present" half of
§13.1: we always run our own, and Bonjour or Avahi being installed is a coexistence question
that the reuse flags already answer.

Under 2 s holds everywhere it was measured, with the whole distribution either under 60 ms or
in a band just past 1 s, and no run over 1.1 s across seventy browses. The margin is one lost
query, so anything that costs a second retry (a switch with IGMP snooping and no querier, a
busy Wi-Fi segment) puts the target at risk. A UI that shows a spinner for 2 s and then says
nothing found will be wrong sometimes; give the browse a longer deadline than the target.

The 63-byte instance name is a real bug waiting to happen and belongs in whichever issue
builds the advertised name, with a test at the boundary.

For #5: nothing here depends on iroh's discovery, so turning it off costs us nothing.

For #33: the Windows firewall rules are installer markup and have to be there on day one,
because the service will never prompt. macOS Local Network consent cannot be pre-granted and
has to be handled as a first-run flow that survives being denied.

Still open, and each needs hardware nobody has here: a real Windows 10 or 11 run, above all
whether `ServiceDaemon::new()` can bind 5353 next to the DNSCache service; a desktop Linux run
with `firewalld` or `ufw` actually enabled; and the unplugged-router test on two machines and a
switch.

## Reproducing

The throwaway harness is `spikes/mdns/`. It has its own `[workspace]` key, so the root
`cargo build --workspace` does not see it. Verified by breaking it on purpose: with a type
error in `spikes/mdns/src/main.rs`, `cargo clippy` in the spike exits 101 and `cargo build
--workspace` at the root still exits 0.

```sh
cd spikes/mdns && cargo build --release
./target/release/rfm-mdns-spike advertise devA 7 60 wsalpha wsbeta &
./target/release/rfm-mdns-spike browse 2 8000
./target/release/rfm-mdns-spike bind5353

# Linux, no Internet and no DNS
docker build --target noavahi -t rfm-mdns:noavahi spikes/mdns
docker network create --internal --subnet 10.77.0.0/24 rfm-airgap
docker run -d --name rfm-a --network rfm-airgap --dns 127.0.0.1 rfm-mdns:noavahi \
  rfm-mdns-spike advertise devA 7 60 wsalpha wsbeta
docker run --rm --network rfm-airgap --dns 127.0.0.1 rfm-mdns:noavahi \
  rfm-mdns-spike browse 2 8000

# Linux, with Avahi on the same host
docker build --target avahi -t rfm-mdns:avahi spikes/mdns
```

Versions: `mdns-sd` 0.21.3, Rust 1.98.1, macOS 15.7.3 (24G419) arm64, Docker 29.4.0 under
OrbStack with kernel 7.0.14-orbstack aarch64, Debian 12.15 containers, `avahi-daemon` 0.8.
