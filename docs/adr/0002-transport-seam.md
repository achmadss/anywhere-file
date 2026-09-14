# 0002 — The transport seam

**Status:** accepted, 2026-09-13. Follows from [0001](0001-rust-iroh-go-control-plane.md).

## Decision

`rfm-core` may use iroh for **five things and nothing else**. Everything above that line is
written against an abstract bidirectional stream.

### The five

| # | Capability | Who uses it |
|---|---|---|
| 1 | **Bind an endpoint** to a long-lived Ed25519 secret key, advertising ALPN `rfm/1`. | #18 |
| 2 | **Dial a peer by its public key** and get a connection back, without knowing an address. | #18 |
| 3 | **Accept** an inbound connection and learn the dialing peer's public key, **authenticated by the handshake**. | #18, #14 |
| 4 | **Open and accept bidirectional streams** on a connection. | #15 |
| 5 | **Observe the current path** for a connection — direct or relayed, and which relay — as it changes. | #42, #34 |

Item 3 is the load-bearing one. The access rule (r3 §9) reduces to nothing if the peer key
it is handed can be spoofed, so `rfm-core` takes the key from the TLS handshake and never
from anything the peer sends inside the stream. #47 states this as a claim to defend and #14
tests it.

Item 5 is the one still unproven. `transport.Watch` gave it to us for free under libp2p;
under iroh it is #42's job to find the equivalent and write it down here.

### What is not in the seam

Configuration is not: relay maps, discovery services, keep-alive and timeout tuning,
congestion control. Those are the agent's to set at startup (#33) — `rfm-core` receives an
already-bound endpoint. That keeps the swap from dragging iroh's whole configuration surface
along with it.

## Shape

One trait, implemented once, in `device/core/src/transport/`:

```text
Transport         connect(peer) -> Connection ;  accept() -> Connection
Connection        peer() -> PublicKey ;  open() -> Stream ;  accept() -> Stream
                  path() -> watcher of Path      // #42 decides the exact shape
Stream            AsyncRead + AsyncWrite
```

Everything outside that directory — the protocol, the access rule, chunking, resume — sees
only these names. The iroh types stay behind them.

## Why bother, given there is one implementation

Normally this would be an unwanted abstraction: one trait, one implementation, no second
caller in sight. Three things make it worth the cost here.

1. **r3 §19 promises the swap is possible.** A promise nobody has tested is not a promise.
   The trait is the test, and it is checked continuously by the compiler rather than
   occasionally by a person reading imports.
2. **0001 names two concrete revisit triggers.** This is not a hypothetical second
   implementation; it is one we have written down the conditions for.
3. **It is the cheap half.** The expensive part of a swap is discovering which of 40 files
   touched the transport. The trait answers that in a `grep` — and if it is ever abandoned,
   abandoning it costs nothing.

The failure mode is a trait that quietly grows an iroh-shaped method until it fits nothing
else. That is the thing to watch for in review, and the reason the five capabilities above
are enumerated rather than described.

## Consequences

- A `Stream` is `AsyncRead + AsyncWrite` and not an `iroh::endpoint::SendStream`. Anything
  QUIC-specific the protocol wants — stream resets, priorities, zero-copy paths — is a
  deliberate widening of this list, argued in review, not an import.
- #15 cannot be written before this trait exists, but it does not need iroh to be finished:
  an in-memory duplex pair satisfies the trait, so the protocol's tests run without a
  network. That is the main practical payoff and it arrives immediately.
- The exact iroh type and method names behind the seam are deliberately absent from this
  record. #18 fills them in when it binds the first endpoint; pinning names we have not
  compiled against would be guessing.
