# 0005: Go for the agent, a native tunnel, opaque sessions

**Status:** accepted, 2026-09-17. Follows the move to `docs/new-arch.md` and replaces
records 0001 to 0004, which reasoned about the earlier peer-to-peer design and were removed
with it.

## Decision

| Piece | Choice |
|---|---|
| Agent | Go. Device key from `crypto/ed25519`, OS keystore through `zalando/go-keyring`, mDNS through `hashicorp/mdns`, local gateway through `net/http/httputil.ReverseProxy`. |
| Control plane | Go, unchanged. One Go module for both, so the request signing code is written once. |
| Client | Kotlin, Compose Multiplatform, targeting Android, Windows, macOS and Linux. |
| Tunnel | The agent opens one outbound TLS connection to the server, authenticated with its device key. The server sends HTTP requests down that connection over HTTP/2 (`golang.org/x/net/http2`), and the agent answers them through the same gateway it uses on the LAN. No Rathole, no sidecar. |
| User sessions | Opaque random tokens, hash stored, looked up on every request. No JWT in the MVP. |
| Payment | Stubbed. A subscription row with a status an operator sets. No provider. |

The mDNS package changed from `grandcat/zeroconf` on 2026-09-17 with #93. Both are pure
Go and the choice is not interesting on its own; the socket is. `zeroconf` binds 5353 with
`net.ListenUDP`, which sets no reuse options, so on macOS the agent registered and then
neither heard nor was heard beside mDNSResponder, measured on the macOS runner.
`hashicorp/mdns` binds with `net.ListenMulticastUDP`, which sets `SO_REUSEADDR` on every
platform and `SO_REUSEPORT` on the BSDs, which is what the requirement to coexist with
Bonjour, Avahi and the Windows resolver actually needs.

## Why Go for the agent

Every job the agent has is covered by the Go standard library or one pure-Go package. The
control plane is already Go, so the agent and the server share the signed-request format
and the same toolchain. The Rust pieces the earlier design had built (keystore, mDNS) were
about 1,300 lines and had no dependency on the parts that were removed, but keeping them
would have kept a third language and a second CI matrix for a small amount of code.

Two findings from the Rust spikes carry over as things to test in Go rather than trust:
the Windows credential store must persist the key to the local machine and never to a
roaming profile, and an mDNS instance name over 63 bytes is silently dropped.

## Why no Rathole

`docs/new-arch.md` names Rathole as one option for the tunnel and says the separate tunnel
credential "may be reduced or removed" if the tunnel can use the device identity directly.
Rathole is a Rust binary the agent would have to ship, supervise and configure on every PC,
with its own secret in a `device_tunnel_credentials` table.

A tunnel in Go removes all of that. The agent already holds a device key and already signs
requests to the server with it. It uses the same key to authenticate the tunnel connection.
The server maps `device_id` to the live connection, exactly as the requirements describe, and
the `device_tunnel_credentials` table is not needed.

The cost is that the tunnel is our code rather than a maintained project's. It is small:
one outbound connection, an HTTP/2 server on the agent side of it, reconnect with backoff,
and a heartbeat. The failure suite (#40) covers the cases that matter.

## Why opaque sessions and not JWT

`docs/new-arch.md` describes user identity as a JWT and lists the claims to validate and the
attacks to test. Every one of those attacks (changed `sub`, changed role, algorithm
confusion, wrong issuer or audience, expiry and replay) is a bug that can exist because the
token carries its own claims. An opaque token carries nothing. The server looks it up, and
the row says who it is and whether it has been revoked.

The control plane already issues opaque tokens, stores only their hash, and revokes them by
row. That is the revocation the requirements ask for, with no signing key to rotate.

JWT would earn its place if the agent had to verify a user without reaching the server,
which is the V2 offline LAN authorization case. The MVP leaves LAN access open, so nothing
needs it yet. If V2 adds it, this record gets a successor.

## Consequences

- The `device_tunnel_credentials` table and the Rathole sections of `docs/new-arch.md` are
  superseded by this record. The tunnel lifecycle section stands.
- The JWT sections of `docs/new-arch.md` read as "session token" for the MVP.
- Two languages in the repository, Go and Kotlin. CI has one Go job and one Kotlin job.
- The agent is a service with no window. The client is the only UI, including for enrolling
  a PC.
