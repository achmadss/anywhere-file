# Threat model

**Status:** written 2026-09-13 for #47, against r3 (revision 3.1), ADR 0001 and 0002, and iroh
`v1.2.0`. Nothing in `rfm-core` or the control plane is implemented yet, so most of this document
describes what the code must do. Where something is already decided in a spec section or already
true in a dependency, it is cited and marked verified.

## The claim being defended

> A workspace is a signed list of device keys. Files stay on devices. The cloud can sell you a path
> to your devices from outside, and can introduce other people's devices to your admin devices, but
> it can never open a file, add a device, or take a workspace away.
>
> r3 §21

Everything below exists to make that sentence survive contact with an attacker.

## How to read this

Each claim and each assumption carries one of three markings.

`verified` means someone read the code or the spec text that makes it true, and the citation is
here. `by design` means a requirement or an issue says it, no code exists yet, and the marking will
become `verified` when the issue's tests exist. `assumed` means the model rests on it and nothing
checks it.

Citations of the form `relay/src/...` point at the upstream iroh tree that `relay/apply.sh` fetches
into `relay/src`. That tree is gitignored and is not part of the repository. Line numbers are from
the tree with `relay/patches/0001-per-connection-rate-limit.patch` applied (the patch from #46,
which lives on `spike/relay-shaping` and is not merged). Where a citation lands in a region the
patch touches, it says so.

---

## 1. Trust boundaries

| Boundary | What crosses it | What must never cross it | Owner |
|---|---|---|---|
| UI bundle to `rfm-core` | Typed in-process calls and an event stream | A socket, an absolute path outside a share, an iroh type | #18, #35 |
| Agent to agent (`rfm/1` on QUIC) | The file protocol, trust-list version exchange, share definitions | An authorization decision taken by anyone except the serving agent | #15, #14 |
| Agent to cloud (HTTPS + JSON) | Association, membership, pairing requests, the trust-list version mirror, endpoint addresses, relay tokens | A file byte, a filesystem path, a trust-list signature | #22, #24, #26 |
| Relay to cloud (the authorize hook) | An endpoint id in a header; a decision and a rate back | Anything that could be read as a grant of file access | #29 |
| Relay to agent (the relay websocket) | Opaque datagrams addressed by destination endpoint id | Plaintext | #28 |
| Agent to OS keystore | The device secret key, used in process to sign | An exportable copy leaving the machine | #2, #6 |
| Operator to everything | Rate, placement, fleet, TTL and window dials | A trust-list write, a share write, a file read | #43 |

Two of these are load bearing in a way the others are not.

The UI-to-core boundary is what makes r3 §7 true, and it is enforced by construction rather than
by review: the bundle has no origin and no network permission, #35 adds a build-time check that
fails on any network API in the bundle, and #18's API surface carries no iroh type and no path
outside a share. `by design`, both issues have it as an acceptance criterion.

The agent-to-agent boundary is where the peer key is established, and the whole access rule
reduces to nothing if that key can be spoofed. ADR 0002 item 3 names this as the load-bearing
capability. It holds in iroh `v1.2.0`. `verified`:

- The dialer pins the remote key. `ServerCertificateVerifier::verify_server_cert` decodes the
  expected endpoint id out of the TLS server name and rejects the handshake unless the presented
  raw public key is bit-for-bit that key
  (`relay/src/iroh/src/tls/verifier.rs:33-74`).
- Only TLS 1.3 with Ed25519 raw public keys is accepted; TLS 1.2 is refused outright
  (`relay/src/iroh/src/tls/verifier.rs:18-24`, `:76-86`, `:132-141`).
- The accepting side derives the peer key from the completed handshake rather than from anything
  the peer sends: `remote_id_from_noq_conn` reads `conn.peer_identity()` and fails closed if it is
  absent or malformed (`relay/src/iroh/src/endpoint/connection.rs:382-408`, exposed as
  `Connection::remote_id` at `:568`).

So `rfm-core` can take the key from the connection and must never accept one from inside a frame.
#15 has to keep it that way, and #14's step 1 depends on it.

---

## 2. Attackers

| Attacker | Assume they can | What they gain if we are right | What stops them |
|---|---|---|---|
| A malicious peer in the workspace | Send anything on `/rfm/1.0.0`, including malformed and hostile frames | Only what a grant gives them, on the shares that name them | #15's framing limits and malformed-input suite; #14 step 6; #13's path jail |
| A revoked device | Reconnect, replay an old trust list, lie about its version | Nothing, once a peer holds the version that revoked it | #8's acceptance rule (a lower version is never adopted); #10's connection-time rejection |
| The cloud, fully compromised | Lie about membership, subscriptions, addresses and relay authorization | Denial of remote access, and metadata. No file, no device, no workspace | #14 step 2 as the only granting step; D5; #24's no-signing rule; #48's hostile-cloud test |
| A compromised relay | See relayed traffic, drop it, reorder it, lie to the authorize endpoint | Traffic analysis and denial | End-to-end TLS 1.3 between endpoints (§1); the relay is a forwarder of opaque datagrams (claim C4) |
| A malicious member | Get a device admitted, then attack from inside | Whatever the admin granted that device, and nothing else | Admin approval with a visible fingerprint (#24 step 3); shares grant per device or per account (#12) |
| Someone on the LAN | Answer mDNS, impersonate, flood discovery | Denial of discovery, and one wasted dial | Principle 5: discovery is not authentication. The dial authenticates the key (§1) |
| A compromised operator account | Use every dial in #43 | Rate, placement and relay availability. No trust list, no share, no file | #43's structural rule that the operator API has no code path to a trust list or a share |

Two attackers deserve their consequences spelled out, because the answer is less comfortable than
the row above suggests.

### The compromised cloud

The cloud can do real damage. It can refuse to hand out endpoint addresses (#26), refuse relay
authorization (#29), delete an association, or hand a device an address it controls so that the
device sends QUIC initials to an attacker and reveals its own IP. All of these are denial or
metadata. None of them reaches a file, because the serving agent's step 2 consults a trust list the
cloud never signed.

The address-substitution case has no mitigation and is not worth building one for: a dialer has to
send packets somewhere, and the handshake fails immediately against the wrong key
(`relay/src/iroh/src/tls/verifier.rs:56-72`, `verified`). It is recorded here as accepted rather
than solved.

### The compromised relay

A relay is the one component that sees every relayed byte, so the precise limit of what it sees
matters.

What it does not see: file contents, filenames, or the file protocol. The relay forwards
`Datagrams { ecn, segment_size, contents }` addressed by destination endpoint id
(`relay/src/iroh-relay/src/protos/relay.rs:198-207`, upstream). The contents are the QUIC packets of
an endpoint-to-endpoint TLS 1.3 session the relay is not a party to, so it cannot decrypt them and
cannot substitute itself for a peer.

What it does see: which endpoint id sends to which, at what times, in what sizes, and each client's
IP address. `verified`. That is the honest limit of r3 principle 6, and it is enough to infer that
two named devices are transferring something large right now.

A relay cannot forge a source. The source endpoint id on a forwarded packet is taken from the
authenticated handshake guard rather than from the frame
(`relay/src/iroh-relay/src/server/client.rs:588-596`, upstream), and the handshake requires a
signature over a server-chosen challenge or over exported TLS keying material
(`relay/src/iroh-relay/src/protos/handshake.rs:11-20`, `:201-228`, upstream).

---

## 3. The claims to defend

### C1. The cloud cannot cause a file to be read

r3 §9 step 2 is the only step that can grant, and it reads the local trust list. Steps 3 to 6 can
only deny or defer. No cloud input appears anywhere in the rule except step 4, which is a deny-only
gate on relayed paths.

Enforced by **#14**, whose acceptance criteria include a test per step and a test that an admin with
no grant cannot read a file. Attacked by **#48** with a hostile control plane.

`by design`. The rule itself was read in r3 §9 (lines 229-251) and in #14's body, which restates it
unchanged.

### C2. A direct-path request is never denied by cloud state

r3 principle 3, and the sentence under §9: *"direct paths (LAN or hole-punched) are never affected
by cloud state."* This is what makes principle 1 (local works with no account, no subscription, no
Internet) survive the addition of remote access.

Enforced by **#14** (step 4 applies only when the path is relayed) and **#32** (the token can only
deny, and its absence denies only relayed requests).

`by design`, with one sharp edge that is now verified and needs a decision. A connection can hold a
relay path and a direct path at the same time, and only one of them is selected for application
data. iroh `v1.2.0` exposes this as `Connection::paths()` returning a list of paths, each with
`is_relay()` and `is_selected()`
(`relay/src/iroh/src/socket/remote_map/remote_state/path_watcher.rs:455-482`;
`Connection::paths()`, `paths_stream()` and `path_events()` at
`relay/src/iroh/src/endpoint/connection.rs:1140-1180`). Step 4 must therefore ask about the
*selected* path. The stricter reading, "any open relay path means treat the request as relayed",
would deny direct-path requests on cloud state and break this claim outright. Raised on #14 and #42.

### C3. The cloud cannot add a device

r3 §8.1: a device accepts version N+k only if it is signed by a key that was `active` and `admin`
in the version it currently holds, and the first version is self-signed. r3 D5 and principle 4 say
the cloud never signs. r3 §8.3 step 3 keeps the signature on an admin device even when the cloud
carried the request and even when the admin has enabled auto-approve.

Enforced by **#7** (verify), **#8** (the acceptance rule, including replay of an older version and
signature by a revoked admin), and **#24** (no cloud code path creates a trust-list entry).

`by design`. #24's body states the rule; its acceptance criteria did not test it, which is gap
G5 below.

### C4. The relay sees ciphertext only

r3 principle 6. `verified` for content, with the metadata caveat in section 2. The relay's own
authorization decision cannot widen this: `Access::Allow` and `Access::AllowLimited` admit an
endpoint to the relay's forwarding service and nothing else
(`relay/src/iroh-relay/src/server.rs:350-370`, patched region).

### C5. A request cannot escape a share root

r3 §9 step 5: checks are performed on the canonicalized, symlink-resolved path and must stay inside
the share root.

Enforced by **#13**, which already names the attack list (`..`, symlinks out, TOCTOU swaps, Windows
short names, alternate data streams, UNC paths, case-insensitive collisions, hardlinks) and requires
a containment suite on all three desktop OSes as a first-class artifact. Attacked independently by
**#48**.

`by design`. This is the single largest piece of unwritten security-critical code in the product,
and it is the one place where a platform difference becomes a vulnerability rather than a bug.

### C6. Revocation is enforced by the peers a device tries to talk to

r3 §8.5. The revoked device need not be reachable. Every peer that holds the new version rejects the
key at connection time, which is what keeps revocation working with the Internet unplugged.

Enforced by **#10** (connection-time rejection, and catch-up over LAN alone), reached by **#8**'s
acceptance rule.

`by design`. Two timing facts belong with this claim rather than buried in the issues. Revocation
propagates at the speed of trust-list propagation (§8.4), so a peer that has not yet seen version
N+1 will still serve the revoked device. And relay de-authorization, which is the cloud's half of
§10.3, does not reach a connection that is already open. That is gap G1.

### C7. A denial does not tell the peer why

#15 requires structured errors on the wire that map to #13's error types and #14's denial steps,
with the step logged locally and a generic denial sent. Without it, step 5 and step 6 become an
oracle for enumerating share roots and grants.

`by design`, in #15's scope.

---

## 4. Assumptions this model rests on

### A1. Relay shaping is receive-side only

The token bucket wraps `poll_read`. `poll_write` is a pass-through to the inner stream with no
bucket consulted (`relay/src/iroh-relay/src/server/streams.rs:565-618` and `:620-627`, upstream and
untouched by the #46 patch). There is no send-side limit to configure, so this is a property of the
design rather than a setting.

r3 §11.3 reasons that one receive-side bucket suffices because a relay cannot emit a byte it did not
first accept. `verified` as a statement about the relay: every byte the relay sends was accepted
from some client's rate-limited read.

### A2. A workspace's aggregate rate is enforced by cloud arithmetic

The relay's bucket is per connection and cannot be keyed by workspace: `RateLimited` owns its
`Bucket` by value and there is one per connection (`docs/spikes/relay-shaping-patch.md`, "The
workspace question"). The cloud divides instead, returning `workspace_rate / active_devices(workspace)`
from the authorize endpoint (#24's comment of 2026-09-13).

The aggregate therefore holds exactly as far as the cloud's device count is accurate, which puts
active device count on the enforcement path rather than on a dashboard. What a workspace gains by
making the cloud undercount its devices is a rate multiplier: with a real device count of R and a
counted device count of C, the workspace's ceiling is `workspace_rate * R / C`. The defence is that
the divisor must be computed over exactly the set of keys the same endpoint would admit, so a key
that is authorized is always counted. Raised on #24 as gap G6.

### A3. A1 holds only while every relayed byte a workspace receives came from its own devices

This is the assumption A1's reasoning quietly depends on, and it is false as the relay is built.

The relay does not know what a workspace is. `Clients::send_packet` looks the destination endpoint
id up in one flat map of connected clients and forwards, with no check that source and destination
belong together (`relay/src/iroh-relay/src/server/clients.rs:243-259`, upstream). Authorization is
per endpoint at connect time and admits that endpoint to the whole relay
(`AccessControl::on_connect`, called once from the handshake at
`relay/src/iroh-relay/src/protos/handshake.rs:492-495`).

So an admitted endpoint can spend its own receive bucket sending datagrams at any other endpoint
connected to the same relay, and because there is no send-side limit, the victim's inbound relayed
traffic is bounded by the *attacker's* rate rather than by the victim's own. #30 pins every device
of a workspace to one home relay for placement reasons, which puts many workspaces on one relay by
design and makes co-residency the normal case rather than an unlucky one.

What this costs an attacker: a paid workspace, and no control over which relay they land on, since
placement is a cloud decision (#30). What it costs the victim: relay capacity and QUIC decrypt work,
with no path to file access, because the injected datagrams fail the victim endpoint's TLS
handshake. Raised on #30 as gap G2.

`verified` as a reading of the relay source. Not measured. Nobody has run this.

---

## 5. Known-weak spots

### W1. The 8-character pairing code (#17)

Eight characters is a small space. The security is carried by the one-time secret in the payload,
and the code is doing addressing. #17's scope already says to be explicit about which is which and
to make the secret long enough to matter, and requires single use, short expiry, and a rate limit on
attempts, all failing closed.

The direction of protection is worth stating, because it is easy to get backwards. The payload
carries the joining device's public key, so iroh already authenticates *which device* the admin
dials. The one-time secret protects the joining device from being adopted by a workspace it did not
mean to join: it proves to the joining device that whoever is offering it a trust list is the party
that read the code off its screen.

### W2. Trust-list conflicts at equal version (#8)

Two admins signing different content at the same version number is reachable whenever admins are
partitioned, and it is normal on a LAN with the Internet down. #8 already says "last admin signature
wins is not automatically right" and leaves the decision open. It is a weak spot until it is
decided, because the failure mode is two halves of a workspace with different membership, each
convinced it is correct.

### W3. The authorize endpoint failing open (#29)

The relay side already fails closed: any error, timeout, non-200 status, or unparseable body maps to
`Access::Deny` (`relay/src/iroh-relay/src/main.rs:296-318` and `:350-381`, patched region; the
error-to-deny mapping is upstream behaviour that the patch preserves). `verified`.

The risk that remains is on our side of the wire: a handler that returns `{"allow": true}` from a
stale cache, or a 200 with a default-constructed body, when the database is unreachable. #29 already
requires fail-closed behaviour and a database-down test.

One thing #29 does not cover: the bearer token is a single shared secret across the fleet
(`relay/src/iroh-relay/src/main.rs:359-363`, upstream). One compromised relay VPS yields a token
that authorizes queries about any endpoint id, and each answer carries the workspace id and its
rate. Raised on #29 as gap G4.

### W4. Resumed transfers re-validating authorization (#16)

A transfer allowed yesterday is not allowed today if the device was revoked or the relay
authorization expired in between. #16's scope calls this "the easiest place in the product to
accidentally build a permission bypass" and requires re-validation through #14 on resume, with a
test that revokes the receiving device mid-transfer.

The interaction with #32 is where the precision has to live. Step 4's "or this session was
authorized at open" exists so a transfer is not killed mid-file, and resume creates a new session,
so a resumed transfer gets no benefit from it. That reading has to be written down, or "resume" will
quietly become a way to extend a 24-hour token indefinitely.

### W5. The operator console's blast radius (#43)

The dials an operator holds are rate, placement, fleet, cache TTL, grace and expiry windows. Every
one of them can deny service to a workspace, and the fleet dials affect live sessions. What an
operator must not have is any path to a trust list, a share, or a file, and #43 already requires
this to be enforced structurally with a test rather than by policy.

The support lookup is the specific hazard: it exists to answer "why is this workspace's relay
authorization failing", which is a legitimate question that pulls customer state into an internal
tool. #43 requires operator reads of customer data to land in the audit log.

---

## 6. Data handling

What the cloud holds, why, and what removes it. Sources: r3 §10.6, §14, #20, #26, #27.

| Data | Why it exists | Removed by | Status |
|---|---|---|---|
| Account id, email | Identifies a person for sign-in and billing | Account deletion (§14, #20) | Gap G3: the spec does not say whether the row is deleted or tombstoned |
| Subscription and billing state | Pays for relay access | No policy stated. Billing records normally have to outlive a deletion request | Gap G3 |
| Workspace association, trust-list version | Tells the relay which keys to admit | Disable remote access (§10.4); 90 days suspended (§10.5); workspace delete (§14) | `by design` |
| Membership rows | Lets a member's devices request to join | Member removal (§10.3); account deletion (§14) | `by design` |
| Device public keys and display names | The relay authorization mirror | Association deletion; revocation propagated by an admin (§10.3) | `by design` |
| Endpoint addresses | Replaces iroh's own address lookup so no third-party DNS or DHT is involved (§11.1) | #26: retained only while associated, deleted on disable | `by design` |
| Relay byte counters per workspace | Capacity planning and owner visibility (§11.3) | No retention stated | Gap G3 |
| Audit events | Every privileged action (§10.6, #27) | #27 makes the log append-only with no delete path | Gap G3: append-only and account deletion have not been reconciled |

Some of that table needs saying in prose, because it is where this product differs from a cloud
file service.

The cloud never receives a file byte, a filesystem path, or a filename (r3 §7). #27 adds the
matching rule for the audit log, with a test asserting no audit payload field can hold a path,
because the audit log is where a path is most likely to leak in by accident.

Endpoint addresses locate a home. A device's address set says where the person lives, at roughly
city granularity from the IP and exactly from a LAN address plus timing. #26 already treats them as
short-lived and association-scoped. They are the most sensitive thing the cloud holds, and they are
held only while remote access is on.

Account deletion does not touch files or device keys. Owned workspaces become local-only and
memberships are removed (§14). A workspace outlives the account that paid for it, which is what
"the cloud can never take a workspace away" means operationally.

---

## 7. Gaps this model found

Every gap here is filed. Nothing in this section is hypothetical, and the model found fewer than a
document this length might suggest: the requirements were written with the central claim in mind,
and most of what looks like a hole is already an open decision inside an existing issue.

| # | Gap | Where it went |
|---|---|---|
| G1 | De-authorization does not reach a live relay connection. `AccessControl::on_connect` is called once during the handshake and never re-run; the `iroh-relay` binary never calls `Clients::disconnect` (`relay/src/iroh-relay/src/server/clients.rs:224-239`, upstream; only tests call it). r3 §10.3's "immediately" and #29's "within one cache TTL" are true for new connections only. The same missing channel blocks #24's plan to call `set_connection_rate_limit` on live connections when a workspace's device count changes. | Filed as #51 |
| G2 | Assumption A3: the relay does not partition workspaces, so receive-side-only shaping does not bound a workspace's inbound relayed traffic. | Raised on #30 as acceptance criteria |
| G3 | Retention is undefined for audit events, and account deletion versus an append-only audit log has not been reconciled. | Raised on #27 and #20 as acceptance criteria |
| G4 | The relay authorize bearer token is one shared secret across the fleet, so one compromised relay yields a fleet-wide query capability. | Raised on #29 as acceptance criteria |
| G5 | #24's body forbids any cloud code path that creates a trust-list entry; its acceptance criteria did not test it. This is claim C3, the central one. | Raised on #24 as an acceptance criterion |
| G6 | The device-count divisor in A2 must be computed over exactly the key set the authorize endpoint admits, or a workspace gains rate by being undercounted. | Raised on #24 as an acceptance criterion |
| G7 | §9 step 4 must read the *selected* path, because a connection can hold a relay path and a direct path at once. The stricter reading breaks claim C2. | Raised on #14 as an acceptance criterion, and on #42 |

Four issues are asked to reference this document by #47's own acceptance: #13, #14, #15 and #17.

---

## 8. What was verified, and what was not

Read and confirmed against source, all in iroh `v1.2.0` at `relay/src`:

- Peer key authentication on both sides of a QUIC connection (`iroh/src/tls/verifier.rs`,
  `iroh/src/endpoint/connection.rs`).
- Path reporting, including `is_relay`, `is_selected`, and change streams
  (`iroh/src/socket/remote_map/remote_state/path_watcher.rs`,
  `iroh/src/endpoint/connection.rs`).
- The relay forwards opaque datagrams, stamps the source from the authenticated handshake, and does
  not constrain the destination (`iroh-relay/src/protos/relay.rs`, `iroh-relay/src/server/client.rs`,
  `iroh-relay/src/server/clients.rs`).
- Rate limiting is receive-side only (`iroh-relay/src/server/streams.rs`).
- The HTTP access mode fails closed and is consulted once per connection
  (`iroh-relay/src/main.rs`, `iroh-relay/src/protos/handshake.rs`).

Read and confirmed against the requirements: r3 §7, §8, §9, §10, §11.1, §11.3, §12, §14, §15, §21,
principles 1 to 7, and D5. Read and confirmed against the issues: #8, #10, #12, #13, #14, #15, #16,
#17, #18, #20, #24, #26, #27, #28, #29, #30, #32, #33, #35, #42, #43, #48, and #46's report at
`docs/spikes/relay-shaping-patch.md`.

Not verified, and not verifiable today:

- Every claim marked `by design`. No `rfm-core` code exists beyond a crate skeleton
  (`core/src/lib.rs`) and no cloud code exists beyond a `main` that prints a line
  (`cloud/main.go`). The access rule, the path jail, the protocol and pairing are all unwritten.
- Everything about a deployed relay. Nothing here was tested against a running `iroh-relay`, and A3
  in particular is a reading of the code rather than a measurement. Testing it needs #28's fleet,
  two workspaces placed on one relay, and a load generator.
- Windows and Linux behaviour of anything. This document was written on macOS arm64, which is the
  only platform available. The path jail (C5) is the claim most exposed to that, since half of #13's
  attack list is Windows-specific.
- The keystore claims implied by the agent-to-keystore boundary. #2 is the spike, and r3 §20.3
  already records the likely tension: iroh signs with the key in process, so a genuinely
  non-exportable key may not be available.
