# Device access and remote application architecture

## Status

Rewritten 2026-09-18 to describe the system as built. It previously described a design still
being chosen, with an amendments block on top listing where
[ADR 0005](adr/0005-go-agent-native-tunnel-opaque-sessions.md) had overruled it. The body now
matches the record, so that block is gone.

Built: the agent, its settings UI (#136), signing a PC in and out of an account (#139, #141),
the control plane, the website's account pages (#137), the downloads (#138), the client's
build for Android and the desktop (#97), its LAN discovery (#98) and its file browser
(#155). Not built: the rest of what the client does (#100 to #103).
Where a section describes something not yet written, it names the issue.

## Overview

An agent on each managed PC runs and exposes local applications, dufs first.

There are two ways to reach one:

1. Local mode. A client on the same network finds the agent and connects to it directly. No
   account and no Internet.
2. Remote mode. A client elsewhere goes through the central server, which checks the session,
   the user's binding to the device and the application, then forwards the request down a
   tunnel the agent keeps open.

LAN access is open on purpose for the MVP. Version 2 can add a local user check without
changing the transport or the application routing.

## Components

```text
PC1 / PC2 / PC3
└── Agent
    ├── Device identity
    ├── LAN discovery
    ├── Local access gateway
    ├── Application registry
    ├── Settings UI and command line   (#136)
    └── Remote tunnel client

Central server
├── Accounts and sessions
├── Device registry
├── User/device authorization
├── Invitations
├── Application permissions
├── Audit logs
└── Remote tunnel endpoint

Website                                (#137, #138, #139)
├── Signup, email verification, password reset
├── Device approval
└── Downloads

Client                                 (#97 to #103)
├── LAN discovery
├── Local connection
└── Remote connection
```

The client is installed on whatever the person carries and only consumes. It never configures
a PC. That is the agent's own job, on the PC being shared.

## Identity model

Users and devices have separate identities, and neither stands in for the other.

### User identity

A session is an opaque token. The server hashes it and looks the hash up in `sessions` on
every request, so nothing is trusted because it decodes.

- Created by `POST /v1/auth/signin`, returned once, stored only as a hash.
- Carried as `Authorization: Bearer` or as the session cookie the website sets.
- Revoked by deleting the row, which takes effect on the next request.

There is no JWT anywhere. An earlier draft of this document described one; ADR 0005 chose
opaque tokens so that revocation does not wait for an expiry.

### Device identity

The agent generates one Ed25519 key per PC on first run and keeps it for the life of the
machine. The private half never leaves.

```text
Agent
├── private key  ← the OS keystore, or a mode 0600 file where there is none
└── public key   → registered with the server at enrolment
```

`device_id` is the SHA-256 digest of the public key, 64 hex characters. The server derives it
in the schema, as a generated column, so an agent cannot choose its own:

```sql
device_id text PRIMARY KEY
  GENERATED ALWAYS AS (encode(sha256(decode(public_key, 'hex')), 'hex')) STORED
```

A `device_id` is a name. A device proves itself by signing, over a nonce and a timestamp the
server records once in `device_nonces` so the same request cannot be replayed.

Replacing the key makes a new device and drops the PC out of every binding it had.

## Authentication and authorization

Authentication asks who this is. Authorization asks whether they may reach this device and
this application. The two are separate checks against separate tables.

```text
Remote request
    ↓
session  → account
    ↓
device_users   (this account, this device, revoked_at null)
    ↓
device_apps    (this device, this app name)
    ↓
subscription of the device's owner
    ↓
the live tunnel for this device
```

Any miss is a denial with a generic body. The reason is logged and audited; the client sees
only the status.

## The agent on a PC

### What it shares

`agent.json` in the agent's directory lists what this PC offers, and is written empty on
first run. A PC that shares nothing is a working state: it announces itself and offers no
applications.

```json
{
  "name": "pc1",
  "apps": [
    {
      "name": "files",
      "type": "http",
      "address": "127.0.0.1:5000",
      "command": ["dufs", "/home/ana/Shared", "--bind", "127.0.0.1", "--port", "5000",
                  "--path-prefix", "/files", "--allow-all"]
    }
  ]
}
```

The address stays on the PC. Only the name and the type are ever sent to the server.

### Choosing what to share

The agent serves a settings page on `127.0.0.1` and takes the same commands from a terminal
(#136). `agent settings` opens the page, and a `.desktop` entry on Linux runs that. The macOS
menu bar item and the Windows tray icon are #142. Both front ends go through one endpoint on
the running agent, so the agent stays the only writer of `agent.json` and picks up a change
without being restarted.

Loopback is not on its own an authorization, because every account on a shared PC can reach
it, so the endpoint takes a token from a mode 0600 file in the agent's directory.

### Running an application

An entry with a `command` is an application the agent runs, restarts when it exits, and stops
with itself. Its output goes to the agent's log, because a service has no window. An entry
with no command is something else's process that the agent only forwards to.

The address of an application the agent starts has to be a loopback one. An application
listening on the LAN could be reached around the gateway, which would make everything the
gateway refuses reachable anyway.

## Local LAN access

### MVP behavior

No account and no Internet connection. The client browses for `_anywhere-file._tcp`, and the
record carries the gateway port in SRV and the device id, the display name, the application
names and the protocol version in TXT.

```text
Client
  │ mDNS / DNS-SD
  ▼
PC1 Agent   192.168.1.20:7433
  │         device_id: 4f3a...  (64 hex)
  │         apps: files
  ▼
dufs
```

`GET /.well-known/anywhere-file` on the gateway is the authoritative list, and is what the
client reads after it finds a PC.

### MVP authorization

Open, on purpose. Anyone who can reach the agent on the LAN can use the applications it
exposes. Network access to the LAN is the access boundary for the MVP, and this is written up
as accepted risk A1 in [`security/threat-model.md`](security/threat-model.md).

What the MVP still guarantees is the surface. The gateway resolves a logical name from the
registry to a loopback address and refuses anything else, so there is no way to name a host,
a port or a scheme in a request.

### V2 local authorization

A local user check at the agent, against the central service when the Internet is reachable
or against a local database if offline authorization is wanted. The application routing and
the transport do not change.

## Local discovery and trust

Discovery says where the agent is. Trust says whether it is the expected one. mDNS answers
only the first and carries no key.

The gateway serves HTTPS with a certificate it signs itself. There is no authority to check
it against, so the device key is what a client trusts instead:

- The certificate carries a P-256 key derived from the device key's seed, the same after
  every restart. It is derived because Schannel on Windows, LibreSSL on macOS and every
  browser engine refuse an Ed25519 certificate outright.
- The discovery document carries the Ed25519 public key and that key's signature over the
  certificate's public key.
- A client checks that the digest of the public key is the `device_id` it wanted, and that
  the signature covers the certificate the connection is actually using.

Another PC can copy the document and cannot serve a certificate to match it. The client half
of this is #127, which pins the device key the way ssh pins a host key.

## Enrolment

A PC works on the LAN with no account. Enrolling it adds remote access.

The person approves the PC in a browser while signed in to the website. Both halves are
built: the server's (#139) and the agent's, which asks and then polls (#141). This is the
OAuth device authorization grant in shape, and it is that shape because a `device_id` is not
a secret: it travels in the mDNS record and the discovery document, so a link carrying only a
device id would let anyone who has seen a PC enrol it to their own account.

```text
Agent                          Server                    Person
  │ start, signed with            │                         │
  │ the device key                │                         │
  ├──────────────────────────────►│                         │
  │ ◄──── code + URL ─────────────┤                         │
  │                               │                         │
  │ open the browser              │   sign in, approve PC1  │
  │ (or print the code)           │◄────────────────────────┤
  │                               │                         │
  │ poll ────────────────────────►│                         │
  │ ◄──── enrolment token ────────┤                         │
  │                               │                         │
  │ enrol, signed ───────────────►│                         │
```

Approval alone binds nothing and the device key alone binds nothing.

The server address and the device id are written to `agent.json` only after the server
accepts, so a refused or expired approval leaves the PC as it was. `agent enrol` on the
command line still takes a token minted elsewhere, for a PC being set up from a script.

Signing a PC out posts to `/v1/devices/unenrol`, signed with the device key, and clears the
server and device id locally. A PC can always remove itself, and the signature is what proves
it is that PC.

## Remote access

The agent keeps one outbound connection to the server open, so a PC needs no public address,
no inbound port and no port forwarding.

The tunnel is ours. Rathole was considered and dropped in ADR 0005, because it is a Rust
binary the agent would have to ship, supervise and configure on every PC, with its own
credential separate from the device key. There is no `device_tunnel_credentials` table.

```text
Agent                                  Server
  │ POST /v1/tunnel, signed               │
  ├──────────────────────────────────────►│
  │ ◄──── 101, connection handed over ────┤
  │                                       │
  │ the roles swap: the server is now the HTTP/2 client
  │ on this socket, and the agent's gateway answers
  │                                       │
  │ ◄──── request for /files/a.txt ───────┤
  ├──── response ────────────────────────►│
```

The same handler answers the tunnel and the LAN, so what the LAN cannot reach the server
cannot reach either.

The connection drops whenever the network does. The agent dials again, waiting a second and
doubling to a minute, spread out so a server coming back does not take every agent it dropped
in one instant. The server pings every 30 seconds, and 90 seconds of silence counts as gone
from both ends, because a broken path can leave a socket looking open for minutes.

## Remote request flow

```text
Ewax
  │ HTTPS, session token
  ▼
Central server   GET /d/<device_id>/files/holiday/a.txt
  │
  ├── look up the session hash
  ├── device_users for this account and device, revoked_at null
  ├── device_apps for this device and "files"
  ├── the subscription of the device's owner, the first admin it ever had
  └── the live tunnel for this device
           │
           ▼
      the tunnel
           │
           ▼
       PC1 Agent gateway
           │
           ▼
         dufs
```

The response comes back the same way. The client never holds anything of PC1's.

The server terminates TLS and forwards in plaintext down the tunnel, so it can read and alter
every remote request. That is accepted risk A2, and it is the cost of a server that authorizes
per request.

## Application routing

Applications are named, so that the remote API cannot become an open proxy or an SSRF
primitive:

```text
files
  ↓  looked up in this PC's registry
127.0.0.1:5000
```

The name stays on the path. `/files/holiday/a.txt` reaches the application with `/files`
still on it, because an application has to be told the prefix it is served under or the links
it writes land nowhere. dufs is told with `--path-prefix`.

The server stores the name and the type in `device_apps` and never the address.

## User/device binding

A device is bound to accounts, one row per pair, with a role.

```text
PC1 ← Susi   role = admin
PC1 ← Ewax   role = guest
```

The first admin a device ever had is its owner, revoked or not, because that is the account
the subscription belongs to. Enrolment always leaves an admin row, so a device with no owner
is a device nobody pays for.

An admin sees the users on a device, can revoke one, and can invite another account. A guest
sees the device and its applications and none of the management.

## Invitations

An admin creates an invitation for another account, with a role and an expiry. The code is a
bearer credential, so it is high entropy, single use, short lived, stored only as a hash and
kept out of logs and URLs.

Consuming it is one guarded statement, and only the request that wins creates the binding:

```sql
UPDATE invites
SET used_at = now(), used_by = $1
WHERE code_hash = $2 AND used_at IS NULL AND expires_at > now();
```

Two redemptions racing for one invitation leave one binding. There is a test that runs two
transactions at it and fails the build if both get through.

## Revocation

Revocation does not wait for anything to expire.

- A session is a row, and signing out deletes it.
- A binding sets `revoked_at`, and the routing check reads it on every request.
- A device can be disabled, and the tunnel handshake refuses a disabled device.

A request already in flight completes. The next one fails.

## Session and tunnel lifecycle

When an agent connects:

1. Check the signature on the handshake, and the nonce.
2. Refuse a disabled device.
3. Associate the connection with the device id, replacing an older one.
4. Mark the device online.

When it closes, the mapping goes, the device is offline, and a request needing it is refused
with "device offline" rather than a wait. Why a tunnel ended is recorded on the audit row:
replaced, heartbeat timeout, or closed.

A request id correlates a remote request with its response through the logs.

## The website

Small on purpose. It carries no account, billing or device management, because those are
client screens.

The control plane serves the pages itself, with `html/template` and `embed`, so there is no
second deployment and no build step.

- Signup, email verification and password reset, with the mailer that sends both links
  (#137). Built.
- The device approval page, with the enrolment and sign out endpoints behind it (#139).
  Built, and so is the agent's half of the flow (#141).
- The downloads for each operating system (#138). Built: a version tag makes a GitHub
  release with the three packages on it, and `/download` lists them with the visitor's
  system first.

## Database model

```text
accounts                    email, password_hash, email_verified_at
subscriptions               status, set by an operator (#21)
sessions                    token_hash, expires_at, revoked_at
email_verification_tokens   single use
password_reset_tokens       single use
enrolment_tokens            single use, minted by a signed-in account

devices                     public_key, device_id (generated), name, status
device_users                device_id, user_id, role, created_by, revoked_at
device_apps                 device_id, name, type
invites                     device_id, code_hash, role, expires_at, used_at, used_by
device_nonces               one row per signed request, so none is replayed

audit_events                actor, device_id, action, details as jsonb, at
job_heartbeats              name and last_run, so a job that stopped running is visible
```

## Security boundaries

```text
User → session → authorization → device → device key → tunnel or LAN → agent → application
```

A compromise of one layer does not grant authority at another:

- Knowing a `device_id` does not prove anything about a device.
- Holding a session for PC1 does not authorize every application on PC1.
- Being able to discover a device does not prove its identity.
- Reaching the agent on loopback does not mean being allowed to reconfigure it.

## Pentest cases

The adversarial review is #48, run against these once the client exists.

### Sessions

Forged, expired, replayed after sign-out, one account's token used against another's device,
a token in a URL or a log, and a cookie accepted where a bearer header was meant.

### Device identity

Fake `device_id`, a collision, replayed device authentication, a stolen signature, a second
agent running on a copied key, a disabled device reconnecting, duplicate live connections.

### Enrolment

Guess or brute force a user code, approve a code a different agent started, poll someone
else's code, poll after a refusal or an expiry, replay an approval, unenrol a PC that is not
yours, and reuse an enrolment token.

### Local LAN

Unauthenticated access, malicious mDNS advertisements, device impersonation, traffic
interception, application enumeration, and reaching an unregistered application or an
arbitrary host through the gateway. Open access is expected, so what is tested is that the
surface is the registered applications and that the agent is not a proxy.

### The agent's settings endpoint

Reach it from another machine, and reach it as a second local account on a shared PC.

### Invitations

Reuse, expiry, brute force, the redemption race, a revoked device, a revoked creator,
privilege escalation through the role, and codes leaking through logs or URLs.

### Remote tunnel

Unauthenticated connect, an unauthorized device, a stale tunnel, a duplicate tunnel,
hijacking, cross-device routing by URL, request id confusion, and a request for an
application the device does not have.

## What is in the MVP

Local mode is the agent through #93 plus the client through #155: two PCs and a phone on
one network, no account, no Internet.

Remote mode is everything else, which is accounts, enrolment, bindings, invitations, the
tunnel and remote routing. An earlier draft of this document put all of that in a version 2.
The breakdown in #41 does not, and #41 is what the work follows.

Stubbed or accepted for the MVP:

- Payment is a status column an operator sets.
- LAN access is open. Threat model A1.
- The server sees remote traffic in plaintext. Threat model A2.

## Target architecture

```text
                         Central server
                    ┌────────────────────┐
                    │ Accounts, sessions │
                    │ Authorization      │
                    │ Device registry    │
                    │ Invitations        │
                    │ App permissions    │
                    │ Audit logs         │
                    └─────────┬──────────┘
                              │
                        our own tunnel
                    one outbound TLS connection,
                    HTTP/2 from the server
                              │
                    ┌─────────▼──────────┐
                    │ PC1 Agent          │
                    │ Device identity    │
                    │ LAN discovery      │
                    │ Local gateway      │
                    │ App routing        │
                    │ Settings           │
                    └───────┬────────────┘
                            │
                    ┌───────┴────────┐
                  dufs          other apps


Local:   Client ── mDNS ──► PC1 Agent ──► Application
Remote:  Client ── HTTPS ──► Server ── tunnel ──► PC1 Agent ──► Application
```

The local path is independent of the Internet. Version 2 can add LAN authorization at the
agent boundary while keeping the same device identity, application routing and transport.
