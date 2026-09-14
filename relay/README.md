# relay

The relay is `iroh-relay`, upstream's binary, configured rather than written (r3 §11.3) —
with one exception. Upstream's `limits.client_rx` is a single service-wide, receive-side
rate with no per-endpoint variation. Per-workspace bandwidth shaping is a product
requirement, so it needs a patch. That patch is what lives here.

#46 answered whether that patch is small: yes, 209 production lines against `v1.2.0`. The
prototype is `patches/0001-per-connection-rate-limit.patch` and the reasoning is in
[`docs/spikes/relay-shaping-patch.md`](../docs/spikes/relay-shaping-patch.md). #30 continues
from there.

The patch also fixes two things stock `iroh-relay` cannot do that r3 §11.3 assumes: it makes
the access endpoint's response JSON rather than the bare text `true`, and it lets that
response carry a rate. Deploy the patched binary, never the stock one.

## What is versioned

| | |
|---|---|
| `IROH_VERSION` | the upstream tag we build against |
| `patches/*.patch` | our diff, applied in filename order |
| `src/` | **not versioned.** Upstream's tree, fetched on demand, gitignored. |

Carrying a diff rather than a fork is deliberate: it keeps the size of what we own visible
in `git diff --stat`, and it makes an upgrade a merge conflict rather than an archaeology
project. If the diff ever grows past roughly a file or two, that is the signal to stop
patching and reconsider — either upstream the change or vendor properly.

## Build

```sh
relay/apply.sh                                  # fetch pinned iroh + apply patches
cargo build --release --manifest-path relay/src/Cargo.toml -p iroh-relay
```

## Upgrading iroh

1. Bump `IROH_VERSION`.
2. `rm -rf relay/src && relay/apply.sh`.
3. If a patch conflicts, `apply.sh` stops with the reject. Fix it in `relay/src`, then
   regenerate: `git -C relay/src diff > relay/patches/NNNN-name.patch`.
4. Re-run the shaping test from #30. A patch that still applies cleanly is not proof it
   still does the same thing — upstream can move the code the hook hangs off without
   touching the lines the patch names.
5. Commit `IROH_VERSION` and the regenerated patches together.

CI runs step 2 on any change under `relay/`, so a patch that has stopped applying is caught
on the PR that breaks it rather than at deploy time.

## Deployment

`deploy/` holds everything to run the fleet. Two regions ship as examples;
add more by copying a region env file.

| Path | What |
|---|---|
| `deploy/Dockerfile` | image with the patched binary. Built from `relay/` as context. |
| `deploy/compose.yml` | one relay per VPS, host networking, restart policy, healthcheck, rotated logs. |
| `deploy/relay.toml.template` | the relay config. Rendered per region, never edited per region. |
| `deploy/regions/*.env.example` | operator values per region. Copy to `.env`, fill in, never commit. |
| `deploy/render-config.sh` | renders the template with a region env file and validates the result. |
| `deploy/provision.sh` | firewall, render, start, smoke checks for one VPS. |
| `deploy/capacity/bench.sh` | capacity baseline harness. See below. |

Per-region deploy:

```sh
cp relay/deploy/regions/eu-west.env.example relay/deploy/regions/eu-west.env
# fill in eu-west.env, then on the VPS, from relay/deploy:
REGION_ENV=regions/eu-west.env MONITOR_SUBNET=10.0.0.0/16 ./provision.sh
```

The operator fills in three things per region: the public hostname, the
LetsEncrypt contact address, and the private metrics address. Everything else
has a default in the `.env.example` files.

TLS terminates at the relay on 443 with `cert_mode = "LetsEncrypt"`. Port 80
stays reachable for issuance. Metrics bind the private address from the env
file. The firewall opens 80 and 443 to the Internet and 9090 to the monitoring
subnet only.

`access` starts as `Everyone`. That admits every endpoint id with no check.
It is for first boot and load testing only. Do not ship production on it.
Issue #29 replaces it with HTTP delegation (`access.http.url` in the
template). The template carries the same warning where the operator will see
it.

Note on r3 §11.3: the requirements text shows the HTTP access config as a
table with `url` and `bearer_token`. Upstream syntax is `access.http.url` and
`access.http.bearer_token`. The template uses the upstream form.

`fleet/` is a small standalone crate (own workspace, so the root build
ignores it). It holds the relay mode agents get (`Custom` with our URLs and
nothing else), the public-relay deny list, the throughput harness, and the
acceptance tests.

## Capacity baseline

Issue #30 sizes relays in bytes per second, so the input has to be measured
throughput per instance, not connection counts. Run from a client machine:

```sh
relay/deploy/capacity/bench.sh https://relay-eu-west.example.com
```

It prints one JSON line per round and a summary with the median sustained
bytes per second. Add clients until the rate plateaus. The plateau, with the
VPS size and date, is the number #30 uses. Re-run after any VPS resize or
iroh upgrade.

## Tests

```sh
cargo test --manifest-path relay/fleet/Cargo.toml
```

`tests/no_public_relay.rs` runs in CI: the agent relay mode holds our relays
only, and no `Default`/`Staging` mode or public relay host appears in
`agent/`, `core/`, `cloud/` or `deploy/`. `tests/bench_smoke.rs` runs the
harness against a local relay in CI.

Two tests need a deployed fleet and stay ignored in CI:

```sh
RFM_RELAY_URLS=https://relay-eu-west.example.com,https://relay-ap-se.example.com \
  cargo test --manifest-path relay/fleet/Cargo.toml --test fleet_transfer -- --ignored --nocapture
RFM_RELAY_URLS=https://relay-eu-west.example.com,https://relay-ap-se.example.com \
  cargo test --manifest-path relay/fleet/Cargo.toml --test relay_failover -- --ignored --nocapture
```

The first moves bytes between two agents through the relay. The second
resumes a transfer across reconnects; kill one relay mid-run to prove the
recovery, or let it run to prove the resume machinery moves the same bytes.

## Version pin and security tracking

`IROH_VERSION` pins the relay. `core` and `agent` pin the same iroh release
in the root `Cargo.toml`, so bump them together.

Watch upstream for security releases: the n0-computer/iroh GitHub releases
page and its security advisories. Treat every `iroh-relay` change as
deploy-relevant until read. iroh 1.0.2 was an `iroh-relay` security fix, which
is why this section exists. On a new release, follow "Upgrading iroh" above,
re-run the fleet tests, re-run the capacity baseline, and redeploy.
