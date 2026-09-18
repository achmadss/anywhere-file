# Threat model

**Status:** written 2026-09-17 against `docs/new-arch.md` and ADR 0005. The agent and the
client do not exist yet and the control plane holds only accounts and sessions, so most of
this describes what the code must do. Each claim is marked `verified` (someone read the code
that makes it true), `by design` (an issue says it, no code yet) or `accepted` (a risk we
are choosing to carry, on purpose, for the MVP).

## What is being defended

A user can reach the applications on their own PCs, and on PCs whose owners invited them,
and nobody else can. The server can see and shape remote traffic; it cannot reach an
application on a PC without an authorized user asking it to.

## Trust boundaries

| Boundary | What crosses it | What must never cross it | Owner |
|---|---|---|---|
| Client to agent, on the LAN | HTTP to a registered application | A request for an unregistered host or port | agent gateway |
| Client to server | Session token, account and device management, remote application requests | A device private key | control plane |
| Agent to server | Enrolment and app registry, signed with the device key; the tunnel | The device private key | control plane, agent tunnel |
| Server to agent, down the tunnel | HTTP requests for a named application | A request the server did not authorize against `device_users` and `device_apps` | remote routing |
| Agent to OS keystore | The device seed | An exportable copy leaving the machine | agent identity |

## Attackers

| Attacker | Assume they can | What they gain if we are right | What stops them |
|---|---|---|---|
| Someone on the LAN | Reach the agent's port, answer mDNS, impersonate a device | Use of every exposed application on that PC. See A1. | Nothing in the MVP. V2 adds local authorization. |
| Anyone on the Internet | Send anything to the server | Nothing without a session | Session lookup on every request, rate limits, single-use invitations |
| A user with a session | Ask for any device and application | Only devices in `device_users` for them, only apps in `device_apps` | The routing check, tested per step |
| A stolen invitation code | Redeem it | One binding, once, before expiry | Hashed codes, atomic single-use consume, short expiry |
| A fake or cloned device | Claim any `device_id` | Nothing | Enrolment and the tunnel are signed with the device key; `device_id` is a name, never a proof |
| The server, compromised | Read and alter remote traffic, refuse service, hand out wrong addresses | Everything a remote user could do, on every enrolled PC. See A2. | Nothing in the MVP. |
| Whoever can edit `agent.json` | Have the agent run any program | Everything that user can do | The registry is mode 0600 in the user's own directory. Editing it already means holding that user's session. |
| A registered application (dufs) | Do whatever it does | Whatever the application allows | Out of scope. The agent forwards; the application's own security is its own. |

## Claims

### C1. The agent is not an open proxy

The gateway resolves a logical app name from its registry to a loopback address and refuses
anything else. There is no way to name a host or port in a request
(`device/agent/gateway.go`, `verified`). The tunnel serves that same handler, so what the
LAN cannot reach the server cannot reach either (`device/agent/tunnel.go`, `verified`).

### C2. A `device_id` proves nothing

Enrolment, app registry updates and the tunnel handshake are signed with the device key, over
a nonce and timestamp the server checks once. The control plane already does this for signed
agent requests (`hosted/control-plane/agent.go`, `verified`). The tunnel handshake is the
same signed request, checked before the connection is handed over
(`hosted/control-plane/tunnel.go`, `device/agent/tunnel.go`, `verified`).

### C3. Remote routing checks the user, the binding and the app on every request

Session, then `device_users` for that user and device with `revoked_at` null, then
`device_apps` for that device and app name, then the live tunnel. Any miss is a denial with a
generic body. `by design`.

### C4. Revocation does not wait for anything to expire

Sessions are rows and are deleted. Bindings set `revoked_at` and the routing check reads it.
A revoked user's next request fails; a request already in flight completes. `by design`.

### C5. The device key never leaves the PC

It lives in the OS keystore and is used in process to sign. The agent refuses to start with a
fresh key when the store is locked or unreachable, because a new key is a new device.
`by design`; the keystore spike in `docs/spikes/keystore.md` measured what each OS does.

## Accepted risks

These are decisions, written down so nobody rediscovers them as surprises.

### A1. LAN access is open

Anyone who can reach the agent on the LAN can use every application it exposes.
`docs/new-arch.md` says this is the MVP product decision, and the LAN is the boundary. What the
MVP still guarantees is C1: the surface is the registered applications and nothing else.
V2 adds a local user check without changing the gateway.

Enrolment is on the same footing. `/enrol` on the gateway takes a server address and a
token from anyone who can reach it, so someone on the LAN can bind the PC to their own
account, and a PC already enrolled can be re-bound. `docs/new-arch.md` asks for enrolment
from the client over the LAN and the MVP has no local user check to gate it with. The
device key never leaves the PC either way, so this is a binding an admin can revoke and
not a key anyone can take.

Related: the agent serves plain HTTP on the LAN in the MVP. Traffic can be read by anyone on
the same network. TLS with the device key and a client that pins it is a separate issue,
after the MVP.

### A2. The server sees remote traffic in plaintext

Remote requests terminate TLS at the server and are forwarded down the tunnel. The server,
or anyone who compromises it, reads and can alter every remote request and response. This is
the cost of a central server that authorizes per request. End-to-end encryption between
client and agent through the server is possible later, and would move authorization to the
agent. Not in the MVP.

### A3. Payment is stubbed

The subscription status is set by an operator. Nothing enforces payment. Remote access is
gated on `status = active`, so the gate exists and the thing behind it does not.

## Gaps

None filed yet. The adversarial review (#48) runs once the agent, the routing and invitations
exist, against the pentest cases in `docs/new-arch.md`, and files what it finds here.
