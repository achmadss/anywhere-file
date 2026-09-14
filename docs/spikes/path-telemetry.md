# #42: iroh path telemetry, direct versus relayed, per connection

Answers r3 §20.1 and fills in item 5 of [ADR 0002](../adr/0002-transport-seam.md), the one
capability that record left unproven.

Everything below refers to iroh **v1.2.0**, the version pinned in `hosted/relay/IROH_VERSION`.
Line references are into `hosted/relay/src/`, which `hosted/relay/apply.sh` fetches. Every claim is
labelled observed (something was run here and the output is quoted) or inferred (read from
the source, with the citation, and not executed). Where a run needed Linux, it happened in a
privileged container on this machine, and the section says so.

## Short answers

| Question | Answer | Basis |
|---|---|---|
| 1. Path reported per connection? | Yes, per path within a connection, with a selected flag. | observed |
| 2. Signalled or polled? | Signalled. A `'static` push stream of events, plus a snapshot stream. | observed |
| 3. Across a network change? | The dead direct path is closed and the relay path is reselected, both as events, 260 ms after the change in a simulated network. | observed, in a Linux container |
| 4. Does a stream survive an upgrade? | Yes. Paths are QUIC multipath paths inside one connection, and one stream carried 256 MiB across the switch. | observed |

## 1. Does iroh report direct versus relayed per connection?

Observed. The unit is a path, and a connection holds several at once.

```rust
let paths: PathList<'_> = conn.paths();          // connection.rs:1154
for p in paths.iter() {
    p.is_relay();      // path_watcher.rs:480
    p.is_ip();         // path_watcher.rs:475
    p.is_selected();   // path_watcher.rs:470, carries application data right now
    p.remote_addr();   // path_watcher.rs:460, TransportAddr::{Relay(url), Ip(sockaddr)}
    p.rtt();           // path_watcher.rs:494
    p.id();            // path_watcher.rs:455
}
```

`TransportAddr` is the three-way discriminant (`iroh-base/src/endpoint_addr.rs:54`):
`Relay(RelayUrl)` carries the relay that would be used, which §9 step 4 needs in order to
match a relay-authorization token to a relay. `Custom` exists for out-of-tree transports and
we bind none, so `is_relay()` and `is_ip()` cover our world.

Details that matter for the §9 gate in #14.

The question is which path is **selected**, never whether an IP path exists. A run here opened
six IP paths in the first 38 ms and closed five of them again, while exactly one carried the
data. The
relay path also stays open for the whole connection as a backup and is only closed at
connection close, so "a relay path is present" is true even on a healthy direct connection.

`paths().iter().find(|p| p.is_selected())` can legitimately return `None`. When a path is
abandoned the watcher clears the selection before the selector runs again
(`path_watcher.rs:202-204`, then `remote_state.rs:625`). Upstream's own test helper writes
`.expect("no selected path")` at `iroh/tests/patchbay/util.rs:378`, which would panic in that
window. #14 must treat `None` as unknown, and the safe reading of unknown for an access
decision is relayed.

There is no endpoint-level equivalent. `Endpoint::remote_info()` (`endpoint.rs:1636`) returns
addresses with an `Active`/`Inactive` usage flag and is explicitly a snapshot with no
watcher (`remote_info.rs:5-7`). The only watcher on `Endpoint` is `watch_addr`
(`endpoint.rs:1281`), which reports our own address. Path telemetry lives on `Connection`.

## 2. Does it signal the change, or only answer a poll?

It signals. This is the answer the plan was most exposed on, and it removes the polling
floor entirely.

```rust
// 'static, moves into a spawned task, one item per change.
let mut events: PathEventStream = conn.path_events();     // connection.rs:1186
while let Some(ev) = events.next().await {
    match ev {
        PathEvent::Opened { id, remote_addr, local_addr } => {}   // path_watcher.rs:58
        PathEvent::Selected { id, remote_addr, local_addr } => {} // path_watcher.rs:80
        PathEvent::Closed { id, remote_addr, last_stats, .. } => {} // path_watcher.rs:68
        PathEvent::Lagged { missed } => {}                        // path_watcher.rs:97
        _ => {}   // #[non_exhaustive]
    }
}

// Or the same state as snapshots, borrowing the connection.
let mut snaps: PathListStream<'_> = conn.paths_stream();  // connection.rs:1167
```

`PathEvent::Selected` is the one #14 and #16 want. It fires only when the selection actually
changes (`path_watcher.rs:234-248`), and it fires in both directions, relay to direct and
direct back to relay, because both go through the same `record_selected`.

Two properties of the stream that the callers have to respect.

Subscribe before you read the snapshot. The event channel is a `tokio::sync::broadcast`, so
a subscription taken first cannot miss a transition that happens while you are reading
`paths()` (`path_watcher.rs:1-7`). The reverse order has a hole in it.

Handle `Lagged`. Channel capacity is 8 (`path_watcher.rs:50`), and a consumer that falls
behind gets one `Lagged { missed }` instead of the dropped events. Recovery is to re-read
`conn.paths()`, which is always current.

### Measured

Observed, on this machine, loopback, macOS arm64, release build. Full source and instructions
in [`spikes/path-telemetry/`](../../spikes/path-telemetry). The example runs a local
`iroh-relay`, dials with the relay address only so the connection is forced to start relayed,
then writes 256 MiB down a single bidirectional stream while watching `path_events()`. A
second task polls `paths()` once a second for comparison.

```
[  1.693ms] connect() returned
[  1.851ms]   paths(): relay  id=0 selected=true rtt=672µs addr=relay:https://127.0.0.1:61989/
[  1.948ms] opened bi stream, writing 268435456 bytes
[  4.722ms] EVENT Opened   id=1 ip:127.0.0.1:54209 (bytes written so far: 1245184)
[  4.778ms] EVENT Selected id=1 ip:127.0.0.1:54209 (bytes written so far: 1245184)
[809.188ms] stream finished, peer acknowledged 268435456 bytes
[809.201ms]   paths(): relay  id=0 selected=false rtt=665.721µs addr=relay:https://127.0.0.1:61989/
[809.201ms]   paths(): direct id=1 selected=true rtt=58.389µs addr=ip:127.0.0.1:54209
```

Relayed to direct, measured from `connect()` returning to the `Selected` event: 3.1 ms,
31.9 ms and 2.1 ms across three consecutive runs. The slow run spent the extra time before
`Opened`, waiting on the transport. The `Opened` to `Selected` gap, which is iroh deciding and
publishing, was 56 µs, 26 µs and 35 µs.

An idle connection upgrades the same way, which matters because the §9 gate has to hold for a
connection that is authorized and then sits there:

```
[  2.069ms] idle mode: no streams, watching for 10s
[  7.137ms] EVENT Opened   id=1 ip:127.0.0.1:57318
[  7.196ms] EVENT Selected id=1 ip:127.0.0.1:57318
[   1.003s] POLL   first 1s sample that sees a direct path selected
```

That last pair of lines is the cost of polling stated as a number: the push stream had the
transition at 7.2 ms, a 1 s poll learned it at 1.003 s, 996 ms late. Since the push stream
exists, neither #14 nor #16 needs a polling interval, and neither issue should state one.
Subscribe to `path_events()` when the connection is accepted or dialled, keep the
last `Selected` as the current path, and re-read `paths()` after a `Lagged`.

Loopback timings are not hole-punch timings. A real cross-NAT upgrade needs STUN, address
exchange over the relay and a punch, and upstream's own harness allows 15 s for it
(`iroh/tests/patchbay.rs:120`). What these numbers do establish is the delay iroh's telemetry
adds once the transport has switched, which is tens of microseconds.

## 3. What does the reported path do across a network change?

Observed, in a simulated network. patchbay, iroh's own harness, builds virtual topologies in
Linux user namespaces and is gated `#![cfg(all(target_os = "linux", not(skip_patchbay)))]`
(`iroh/tests/patchbay.rs:27`), so it does not run on this laptop directly. It does run in a
privileged Linux container, and OrbStack is enough. What follows was run here that way, on
copies of `hosted/relay/src` in the scratch directory, never on the checkout.

```sh
docker run --rm --privileged -v "$SRC:/src" -w /src rust:1.98-bookworm bash -c \
  'apt-get update -qq && apt-get install -y -qq nftables iproute2 >/dev/null &&
   cargo test --release -p iroh --test patchbay <filter> -- --nocapture'
```

`nftables` and `iproute2` are missing from the `rust` image and patchbay needs both. Without
them every test dies with `Error: spawn nft`, which is what the first attempt here did.

### Direct to relay, the degradation case

Upstream has no test for a direct path that dies while the relay is still reachable, which is
precisely the case #14 has to notice, so this spike added one:
[`spikes/path-telemetry/patchbay-degrade.rs`](../../spikes/path-telemetry/patchbay-degrade.rs),
with the instructions for installing it in a copy of the iroh tree in its header. Two devices
behind permissive NATs hole punch to direct, then one is replugged behind a symmetric NAT, so
its direct path is dead while the relay stays up.

```
[t=0]            replug behind a symmetric NAT
[264.666279ms]   Closed ip:198.18.0.13:49172
[264.823775ms]   Selected relay:https://relay.test/
FELL BACK TO RELAY after 264.823775ms
```

Four runs: 264.8 ms, 258.4 ms, 264.0 ms, 262.9 ms from the replug to the `Selected` event
naming the relay. The peer saw the same fallback independently. So a degraded connection is
reported, in that order (the dead path closes, then the relay is selected), and both ends
learn it.

That number is a simulated link change on one machine. Real hardware has to get the link
change out of the OS first, and a Wi-Fi association loss or a sleep and wake will be slower
and messier. Treat 260 ms as a floor.

### An interface that returns unchanged reports nothing

Upstream's `link_outage_recovery_client` passed here too: it takes a device's link down for 5 s
and brings it back. Interesting for us is what did not happen.

```
28.058946Z  holepunched, now killing link for 5s
28.059566Z  selected path: [1] ip:198.18.0.12:45777
33.069558Z  set link up ifname=eth0
33.074852Z  connection recovered after link outage
```

Nothing was emitted between those timestamps. The interface came back with the same address,
no traffic had been attempted while it was down, and the direct path resumed at once. iroh
counted two link changes in that run (`actor_link_change: 2.0` in the metrics dump) and still
reported no path change, correctly, because the path was fine. Silence from `path_events()`
means the path is unchanged, and a §9 re-evaluation is only needed when an event arrives.

### The mechanism, for the cases not reproduced here

Inferred from `iroh/src/socket.rs`. `netwatch::netmon` delivers link changes to
`handle_network_change` (`socket.rs:1656`). A major change rebinds the UDP transports
(`:1660`), resets the DNS resolver, re-runs STUN (`:1667`), and hands the QUIC stack a
`NetworkChangeHint` (`:1697`) that classifies each path: a relay path is always recoverable,
because the relay actor reconnects underneath and the address does not change (`:1716-1721`),
and an IP path is recoverable only while its local IP is still on an interface (`:1722-1729`).
An unrecoverable path is abandoned, which reaches `NoqPathEvent::Abandoned`
(`remote_state.rs:594`), emits `PathEvent::Closed` with final per-path statistics
(`path_watcher.rs:214-219`), and re-runs the selector (`remote_state.rs:625`). That is the
sequence the replug test printed.

The default selector explains why reselection is abrupt. `BiasedRttPathSelector` sorts on
`(tier, biased RTT)`, where IP is primary and relay is backup
(`biased_rtt_path_selector.rs:79-103`). Across tiers it switches immediately. Within a tier it
demands a 5 ms improvement, to avoid flapping (`RTT_SWITCHING_MIN`, `:23`), and IPv6 starts
3 ms ahead (`:19`). After a direct path exists, iroh re-punches at most every 5 s
(`HOLEPUNCH_ATTEMPTS_INTERVAL`, `remote_state.rs:51`), looks for a better path every 60 s
(`UPGRADE_INTERVAL`, `:65`), and stops looking below 10 ms (`GOOD_ENOUGH_LATENCY`, `:54`).

Not reproduced here: a real interface change on real hardware, a device sleeping and waking, a
carrier NAT rebinding, and anything involving two physical machines. Reproducing those needs a
second machine and a manual network change, or the patchbay suite run on a Linux host with the
`patchbay` profile, which runs the whole suite.

## 4. Does a stream survive a path upgrade?

Observed: yes. A transfer does not have to finish the current stream and continue on a new
one. This is the answer #16 needs and it removes the resume-across-path-change work that
r3 §11.4 would otherwise have implied.

iroh v1.2.0 runs QUIC multipath. Paths are members of one connection, streams belong to the
connection, and selection only decides which path carries application data
(`path_watcher.rs:78-87`). When the selection changes, the new path is marked `Available` and
the others `Backup` (`remote_state.rs:728-730`). Nothing in that touches stream state.

Measured directly. The example opens one bidirectional stream while relayed, at 1.9 ms, and
writes 256 MiB. The upgrade lands at 4.8 ms with 1,245,184 bytes already written. The same
stream then completes and the peer acknowledges all 268,435,456 bytes, and the example asserts
that count. Per-path byte counters from the closing events show the split:

```
EVENT Closed id=0 relay:https://127.0.0.1:61989/  tx=67045      rx=18419
EVENT Closed id=1 ip:127.0.0.1:54209              tx=274499877  rx=2218064
```

67 KB went over the relay before the switch and 274 MB went direct after it, on one stream,
with no application involvement. The 1.2 MB the writer had handed to the stream by then was
mostly still in send buffers, which is why the relay counter is smaller than that figure.

For #16 that makes a path change telemetry. Chunking and resume are still needed for a
dropped connection, and a mid-transfer path change is not one of the cases they have to
cover.

## What this means for the plan

For #14 (the access decision rule, r3 §9 step 4), the gate is
`conn.paths().iter().find(|p| p.is_selected())` at decision time, plus a `path_events()`
subscription for the rest of the connection's life. Treat `None` and a post-`Lagged` gap as
relayed until proven otherwise. Re-evaluate on every `Selected`, since an upgrade can lift a
connection out of the relay rule and a degradation can drop it back in without a reconnect.
`Relay(RelayUrl)` gives the relay identity a token has to match. The measured lag between a
direct path dying and the gate being able to act on it was 260 ms in the simulated network,
and there is no polling interval underneath that to make it worse.

If #32 ever wants a device that refuses to be relayed at all, `Builder::path_selector`
(`endpoint.rs:847`) takes a custom `PathSelector`, and a selector that never returns a relay
path enforces it in the transport, below the gate. That widens ADR 0002's list of five iroh
capabilities, so #32 should decide it deliberately.

For #16 (chunked, hashed, resumable transfers, r3 §11.4), there is nothing to move. iroh moves
the bytes to the better path underneath an open stream. What #16 gains is a reason to react at
all: a `Selected` event is a sensible point to re-measure throughput or resize a chunk window,
and `Path::rtt()` and `Path::stats()` are there for it.

For ADR 0002 item 5, the shape behind the seam is a stream of events. A watcher of one value
would drop the per-path close statistics that #18's telemetry wants.
`Connection::path() -> watcher of Path` as sketched in that record should become something
closer to `Connection::path_events() -> impl Stream<Item = PathChange>` plus a
`current_path()` accessor that can answer unknown.

For #18 and #34, `PathEvent::Closed` carries `last_stats` with `udp_tx.bytes` and
`udp_rx.bytes` per path, which is exactly the direct versus relayed byte split r3 §18 wants,
with no counting of our own.

The issue body for #42 was written with placeholders, `{{C10}}` for the §9 step 4 gate and
`{{C12}}` for the §11.4 transfer move. Those resolve by position in the core issue list, where
#5 is the first: the gate is #14, "core: the access decision rule (§9)", and the transfer is
#16, "core: chunked, hashed, resumable transfers". This report uses #14 and #16 throughout.
Read literally as #10 and #12 the placeholders point at trust-list propagation and shares,
neither of which concerns paths.

## Reproducing

```sh
cd spikes/path-telemetry
cargo run --release              # forced relay start, 256 MiB on one stream across the upgrade
SPIKE_IDLE=1 cargo run --release # same, no traffic, plus the 1s-poll comparison
```

The patchbay test in that directory needs Linux and does not run from the repo. Its header has
the container command. Run it against a copy of `hosted/relay/src`, never the checkout.

That directory is throwaway. It has its own `[workspace]` so `cargo build --workspace` at the
repo root does not pull iroh into the tree, and `core/` keeps having no dependencies. Delete
it once #14 and #16 have landed.
