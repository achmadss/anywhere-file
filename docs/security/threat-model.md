# Threat model

**Status:** written 2026-09-17 against `docs/new-arch.md` and ADR 0005, reviewed 2026-09-18.
The agent and the control plane are built. The client is not, so the claims that depend on it
are still ahead of the code. Each claim is marked `verified` (someone read the code that makes
it true), `by design` (an issue says it, no code yet) or `accepted` (a risk we are choosing to
carry, on purpose, for the MVP).

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
| A local user to the agent's settings | What this PC shares, and which account it belongs to | Any change from a second local account on a shared PC | agent settings listener (`device/agent/settings.go`) |
| A browser to the account pages | Signup, a verification link, a password reset | That link's token to another origin, or a line break into a mail header | account pages (`hosted/control-plane/web.go`), `validEmail` in `hosted/control-plane/auth.go` |
| A browser to the approval page | Which PC may join the account the visitor is signed in to | An approval nobody pressed, or one for a code the visitor never saw | `/approve` and `answerEnrolment` (`hosted/control-plane/web.go`, `hosted/control-plane/enrol.go`) |

## Attackers

| Attacker | Assume they can | What they gain if we are right | What stops them |
|---|---|---|---|
| Someone on the LAN | Reach the agent's port, answer mDNS, impersonate a device | Use of every exposed application on that PC. See A1. | TLS and the device key's signature over the certificate stop the impersonation and the reading. Nothing gates the use; V2 adds local authorization. |
| Anyone on the Internet | Send anything to the server | Nothing without a session | Session lookup on every request, rate limits, single-use invitations |
| A user with a session | Ask for any device and application | Only devices in `device_users` for them, only apps in `device_apps` | The routing check, tested per step |
| A stolen invitation code | Redeem it | One binding, once, before expiry | Hashed codes, atomic single-use consume, short expiry |
| Someone who read an enrolment code over a shoulder | Approve it, poll with it | Nothing. The token it mints is handed only to the device key the code was minted for | The device key is part of every lookup in `hosted/control-plane/enrol.go` |
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
`device_apps` for that device and app name, then the owner's subscription, then the live
tunnel. Any miss is a denial with a generic body
(`hosted/control-plane/routing.go`, `verified`).

### C4. Revocation does not wait for anything to expire

Sessions are rows and are deleted. Bindings set `revoked_at` and the routing check reads it.
A revoked user's next request fails; a request already in flight completes
(`hosted/control-plane/bindings.go`, `hosted/control-plane/routing.go`, `verified`).

### C5. The device key never leaves the PC

It lives in the OS keystore and is used in process to sign. The agent refuses to start with a
fresh key when the store is locked or unreachable, because a new key is a new device
(`device/agent/identity.go`, `device/agent/seedstore.go`, `verified`). On a machine with no
keystore the seed is a mode 0600 file in a mode 0700 directory and wider permissions are
refused. The keystore spike in `docs/spikes/keystore.md` measured what each OS does.

### C6. Enrolling a PC needs an approval and the device key, and neither alone

The agent asks for a code, signed with its device key, and the row records the key that
asked. A signed-in person approves that code in the browser, which writes down who answered
and binds nothing. The agent then polls, signed again, and only then is an enrolment token
minted for the account that approved. Every lookup on the way has the device key in it, so a
code read over a shoulder answers the same as a code that was never minted
(`hosted/control-plane/enrol.go`, `verified`).

A code is eight characters, which is about 34 bits, so guessing is bounded by the rate limit
on each route that takes one and by the ten minute expiry rather than by entropy. The page
that names the PC is limited the same way, because it is the one that says whether a code is
live. What is not defended is a person who approves a code they were sent: the page says to
approve only a PC they just asked to sign in, and that is the whole of it (`accepted`).

## Accepted risks

These are decisions, written down so nobody rediscovers them as surprises.

### A1. LAN access is open

Anyone who can reach the agent on the LAN can use every application it exposes.
`docs/new-arch.md` says this is the MVP product decision, and the LAN is the boundary. What the
MVP still guarantees is C1: the surface is the registered applications and nothing else.
V2 adds a local user check without changing the gateway.

Enrolment used to be on the same footing, and is being taken off it. `/enrol` on the gateway
accepts a server address and a token from anyone who can reach the LAN, so someone on the
network can bind the PC to their own account or re-bind one already enrolled. It was there for
the client to call (#101, closed). #139 has built the browser approval: a PC asks, a
person approves it while signed in, and the PC finishes with its own key. #140 removes the
LAN endpoint, which ends this half of A1. The device key
never leaves the PC either way, so what was exposed is a binding an admin can revoke.

LAN traffic is encrypted (#96). The gateway serves HTTPS with a certificate the device key
signed, and the discovery document carries the proof, so a client that knows a device id
can tell that PC from anything imitating it and reading the traffic means holding the
device key. What stays open is who may use the applications: anyone who can reach the agent
still can. The client side of the check is #127.

### A2. The server sees remote traffic in plaintext

Remote requests terminate TLS at the server and are forwarded down the tunnel. The server,
or anyone who compromises it, reads and can alter every remote request and response. This is
the cost of a central server that authorizes per request. End-to-end encryption between
client and agent through the server is possible later, and would move authorization to the
agent. Not in the MVP.

### A3. The settings endpoint trusts the local machine

The agent's settings listener binds `127.0.0.1`, which keeps it off the network and leaves
it reachable by every account on a shared PC. A token in a mode 0600 file in the agent's
directory gates it, which is the same protection the registry and the seed file already have
(`device/agent/settings.go`, `device/agent/seedstore.go`, `verified`). An address off
loopback is refused where the setting is read (`device/agent/config.go`), and a request
arriving under any other host name is refused as well, so a name in somebody else's DNS
pointing at `127.0.0.1` is not a way in from a browser. Anyone who can read another user's
mode 0600 files is already that user or root, and on such a machine the device key is gone
too.

### A4. Payment is stubbed

The subscription status is set by an operator. Nothing enforces payment. Remote access is
gated on `status = active`, so the gate exists and the thing behind it does not.

## Gaps

None filed yet. The adversarial review (#48) runs once the agent, the routing and invitations
exist, against the pentest cases in `docs/new-arch.md`, and files what it finds here.
