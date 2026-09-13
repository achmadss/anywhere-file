# Throwaway: #42 path telemetry

Evidence for [`docs/spikes/path-telemetry.md`](../../docs/spikes/path-telemetry.md). Not a
workspace member, not built by CI, no tests. Delete it once #14 and #16 have landed.

It runs a local `iroh-relay` and two endpoints in one process, dials with the relay address
only so the connection is forced to start relayed, then watches `Connection::path_events()`
while a single bidirectional stream carries 256 MiB across the relayed to direct switch.

```sh
cargo run --release              # transfer across the upgrade
SPIKE_IDLE=1 cargo run --release # idle connection, plus a 1s poll for comparison
```

Both endpoints are on one machine, so the upgrade is over loopback and the timings say how
fast the telemetry reports a switch. They say nothing about how fast a real cross-NAT hole
punch completes.
