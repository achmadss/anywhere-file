# 0001 — Rust core on iroh, Go control plane

**Status:** accepted, 2026-09-13. Supersedes an earlier, unrecorded decision to build the
whole product in Go on libp2p.

## Decision

| Piece | Stack | Note |
|---|---|---|
| `rfm-core` | **Rust** | iroh, trust list, shares, file protocol, access rule. r3 **D1** and **D4** as written. |
| Desktop agent | **Rust** + Tauri | Hosts `rfm-core` in-process; the UI bundle calls it and never opens a socket (r3 §7). |
| Control plane | **Go** | r3 D4: *"a separate service in whatever stack the team prefers."* HTTPS + JSON, PostgreSQL. It never speaks iroh's protocol and never links `rfm-core`. |
| Relay | **`iroh-relay` binary** | Deployed and configured, plus one patch. See 0002 and `relay/README.md`. |
| UI bundle, operator console, customer dashboard | TypeScript | |

The Rust surface is `rfm-core` plus the desktop shell. The cloud and UI work is unaffected
by this decision — it is a decision about one third of the tree, not all of it.

## Context

There is a working Go transport at `achmadss/p2p-transport`: peer identity, LAN discovery,
cross-carrier hole punching, relay fallback, mid-transfer path upgrade, and honest
direct-vs-relayed path reporting. Steps 0–5 are done and measured — LAN 65 MB/s, relayed
3.7 MB/s, and a relayed pair on one LAN moving to the LAN in 10 s and then running at
107.7 MB/s. r3 §19 explicitly permits swapping the transport, so either stack was allowed.

Go was chosen first. This record reverses that.

## What decided it

The relay, on both sides.

r3's old §20.2 asked whether a self-hosted relay can authorize per device key, and treated
the answer as an open risk. It is not open. `iroh-relay` takes an `access` mode in its TOML
config, and one of those modes POSTs to an endpoint of ours with an `X-Iroh-Endpoint-Id`
header, admitting the endpoint on `200` + `true`. TLS via Let's Encrypt, a per-client token
bucket, and a Prometheus endpoint all come as configuration. r3 §11.3 has the full config.

That turns the relay from a component into a deployment. Against it, the Go plan required:

- `heimdall`, a relay we own and operate;
- `bifrost`, a coordinator designed in `FLEET.md` and **not built** — leases, placement, a
  fleet protocol, and a subject admin API;
- and, for per-subject bandwidth, a **fork of libp2p's circuit relay** (`FLEET.md` §4.5),
  because upstream's `WithLimit` and `WithMetricsTracer` cannot express a per-subject rate.

### The part of that argument that does not hold

**The fork is not a difference between the stacks.** Per-workspace bandwidth shaping is a
product requirement (r3 §11.3), and upstream `iroh-relay` cannot express it either:
`limits.client_rx` is one service-wide, receive-side rate with no per-endpoint variation and
no send-side limit. A relay patch is needed on *either* stack — that is issue #30, and
issue #46 is the spike that finds out how big it is.

**Authorization was never the differentiator either.** go-libp2p's
`relay.WithACL(ACLFilter)` with `AllowReserve`/`AllowConnect` is upstream, and `FLEET.md`
§4.5 already said it was usable as-is.

What iroh genuinely removes is everything *around* the authorization hook: the relay binary
itself, TLS termination, relay selection, rate-limit plumbing, metrics, and the coordinator.
iroh ships all of that. The Go plan builds it. That is the decision.

## What it is not justified by

Not the hole-punch rate. n0 report ~90–99 % direct; the ~70 % figure r3 §5 cites for libp2p
is a published third-party number; `p2p-transport`'s own step 3 is a pass/fail verdict rather
than a rate. None of those three numbers is comparable to the others, and nothing here rests
on them.

## Consequences

- `p2p-transport` becomes reference rather than foundation. Steps 0–5 are sunk cost.
- r3 §20.3 (path telemetry) was solved by `transport.Watch`, which pushes path changes off
  libp2p's own connection hooks. Under iroh it is an unverified risk again — #42.
- Rust sits on the hottest path in the product, with the learning curve that implies.
- One workspace pins to one home relay, which is what makes its token bucket local and
  deletes `bifrost`'s cross-relay allowance loop, leases, and placement negotiation.
- Three languages in one repo. #45 pays for that with per-language CI.

## When to revisit

0002's seam is what keeps this reversible, and it only stays real if #15's protocol is
written against an abstract stream rather than an iroh type. Two triggers:

- **A pure-Go iroh reaching maturity.** [`tmc/go-iroh`](https://github.com/tmc/go-iroh) is a
  clean-room port targeting wire compatibility with pinned upstream releases. Today: 52
  stars, one maintainer, explicitly *"not stable before v1"*. At v1 with more maintainers it
  would give one language across the whole product **and** iroh's relay.
- **`iroh-go` gaining Windows and macOS.** The FFI bindings
  ([`iroh-go`](https://git.coopcloud.tech/decentral1se/iroh-go/src/branch/main), linked from
  iroh's own docs) are Linux x86_64/aarch64 musl only, which does not cover r3 §2's desktop
  targets.

Official iroh bindings are Python, Node.js, Swift and Kotlin. Go is not among them.
