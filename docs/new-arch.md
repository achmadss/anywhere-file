# Device access and remote application architecture

## Amendments

Decided 2026-09-17, recorded in [ADR 0005](adr/0005-go-agent-native-tunnel-opaque-sessions.md).
Where this document and that record disagree, the record wins.

- The Agent is Go. The client is Kotlin with Compose Multiplatform, for Android, Windows, macOS and Linux.
- The tunnel is our own: one outbound TLS connection from the Agent, authenticated with the device key, carrying HTTP/2 from the server. Rathole is not used and `device_tunnel_credentials` does not exist.
- User sessions are opaque tokens looked up on the server. The JWT sections below describe V2 offline LAN authorization, not the MVP.
- Payment and billing are stubbed: a subscription row with an operator-set status.
- Two risks are accepted for the MVP and written into `security/threat-model.md`: LAN access is open to anyone who can reach the agent, and the server sees remote traffic in plaintext. The LAN is encrypted with the device key (#96).

## Overview

The system installs an Agent on each managed PC. The Agent can run and expose local applications such as dufs.

The architecture has two access modes:

1. **Local mode:** a client on the same LAN discovers and connects directly to the Agent. No Internet connection or user account is required for the MVP.
2. **Remote mode:** a client outside the LAN connects through the central server. The server authenticates the user, checks authorization, and routes traffic through a persistent outbound tunnel such as Rathole.

LAN access is intentionally open in the MVP. Version 2 can add user/device authorization to the local path without changing the underlying transport and application routing.

## Components

```text
PC1 / PC2 / PC3
└── Agent
    ├── Device identity
    ├── LAN discovery
    ├── Local access gateway
    ├── Application registry
    └── Remote tunnel client

Central server
├── User authentication
├── Device registry
├── User/device authorization
├── Invitations
├── Application permissions
├── Audit logs
└── Remote tunnel endpoint

Client
├── Browser / Chrome extension / application
├── LAN discovery
├── Local connection
└── Remote connection
```

## Identity model

There are separate identities for users and devices.

### User identity

Remote access uses a user account and JWT.

```text
JWT
└── sub = user_id
```

The server must verify the JWT signature and relevant claims before trusting its contents.

A JWT payload can be decoded by anyone who has the token. Decoding does not authenticate the user.

Recommended claims to validate include:

- `sub`
- `exp`
- `nbf`
- `iss`
- `aud`
- `iat` when applicable
- token type / scope when applicable

Use a stable user ID in `sub` rather than relying on a username.

### Device identity

The Agent generates a public/private key pair during enrollment.

```text
Agent
├── private key  ← stays on the device
└── public key   → can be registered with the server
```

The private key must not be uploaded to the server.

The server can store the public key or certificate associated with the device.

A device proves possession of its private key by signing a server challenge or through a protocol such as TLS/mTLS.

A `device_id` is an identifier. It is not, by itself, an authenticator.

### Koi

Koi can be used for Agent-side device identity, discovery, and local trust. The client does not have to depend on Koi for the MVP.

The local client can use mDNS/DNS-SD to discover an Agent and then establish a secure connection. If a future design requires the client to participate directly in Koi's trust or mTLS model, Koi can be added to the client later.

## Authentication and authorization

Authentication and authorization are separate.

Authentication answers:

> Who is this user or device?

Authorization answers:

> Is this user allowed to access this device or application?

For remote access:

```text
User JWT
    ↓
User identity
    ↓
Authorization database
    ↓
User → Device → Application
```

For device authentication:

```text
Device connection
    ↓
Device credential / certificate
    ↓
Device identity
```

The server should never accept a client-supplied user ID, device owner, or role as proof of authorization.

## Device registration

When the Agent is installed, it generates its device identity.

Example:

```text
device_id = dev_pc1_abc123
public_key = ...
```

The server can store:

```text
devices
--------------------------------
id
device_id
public_key
name
created_at
status
```

The Agent keeps the private key locally.

If Koi is used, its device identity can be associated with the application's `device_id`.

## Local LAN access

### MVP behavior

The MVP does not require an account or Internet connection for local access.

A client discovers the Agent using LAN discovery such as mDNS/DNS-SD.

```text
Client
  │
  │ mDNS / DNS-SD
  ▼
PC1 Agent
  │
  ▼
Application
```

The client can learn the Agent's local address and available applications.

Example:

```text
PC1 Agent
address: 192.168.1.20
port: 12345
device_id: pc1
apps:
  - file-manager
```

The client then connects directly to the Agent.

```text
Client
  │
  │ LAN
  ▼
PC1 Agent
  │
  ▼
dufs
```

No central server is required.

### MVP authorization

For MVP, LAN authorization is intentionally open:

```text
LAN client → Agent → application
                 ↓
               allow
```

Anyone who can reach the Agent on the LAN can use the applications exposed by the Agent.

This is an explicit product decision. Network access to the LAN is the access boundary for the MVP.

The Agent should still use a secure transport and device identity where practical. LAN discovery itself should not be treated as cryptographic proof of identity.

### V2 local authorization

Version 2 can add user/device authorization:

```text
Client
  │
  │ local authentication
  ▼
PC1 Agent
  │
  │ authorization
  ▼
User → Device → Application
```

The Agent can check the central authorization service when Internet connectivity is available, or a local authorization database can be introduced if fully offline authorization is required.

The application routing and transport layers do not need to change.

## Local discovery and trust

Discovery and trust are separate concerns.

mDNS/DNS-SD answers:

> Where is the Agent?

A certificate, public key, pairing code, or other trust mechanism answers:

> Is this the expected Agent?

A simple future pairing flow could use a PIN or QR code:

```text
Client                    PC1 Agent
  │                          │
  │ discover PC1             │
  ├─────────────────────────►│
  │                          │
  │ receive device identity  │
  │◄─────────────────────────┤
  │                          │
  │ pairing request          │
  ├─────────────────────────►│
  │                          │
  │ user approves / PIN      │
  │                          │
  │◄──── paired ─────────────┤
```

After pairing, the client can retain the necessary local credential.

This is optional for the MVP if the product intentionally accepts any LAN client.

## Remote access

The Agent maintains an outbound persistent connection to the central server through a reverse tunnel.

Rathole can be used as the tunnel transport.

```text
PC1 Agent / Rathole client
          │
          │ outbound connection
          ▼
     Central server
          │
          │ remote traffic
          ▼
       Client
```

The PC does not need a public IP or an inbound Internet port.

The server maintains a mapping between the device and its active tunnel.

```text
device_id              connection
------------------------------------
dev_pc1_abc123         tunnel #42
dev_pc2_def456         tunnel #43
```

Rathole is responsible for maintaining the tunnel. Application-level user authorization remains the responsibility of the application/server architecture.

## Remote request flow

Suppose Ewax wants to access PC1's file manager.

```text
Ewax
  │
  │ HTTPS + JWT
  ▼
Central server
  │
  ├── verify JWT
  ├── identify user = ewax
  ├── check device_users
  ├── check application permission
  └── find active PC1 tunnel
           │
           ▼
      Rathole tunnel
           │
           ▼
       PC1 Agent
           │
           ▼
      file-manager
```

The response travels back through the same tunnel.

The client does not need PC1's private key for this flow.

## Application routing

Applications should be represented logically rather than exposing arbitrary host/port access to remote users.

Example:

```text
device_apps
--------------------------------
device_id
name
type
config
created_at
```

Example:

```text
device_id = pc1
name = file-manager
type = dufs
config = 127.0.0.1:5000
```

The Agent resolves:

```text
file-manager
    ↓
127.0.0.1:5000
```

This prevents the remote API from becoming an unrestricted reverse proxy or SSRF primitive.

The Agent should expose only registered applications and approved operations.

## Device tunnel credentials

Rathole may use its own tunnel authentication mechanism.

If the server needs to retrieve a secret later, store it encrypted rather than as a one-way hash.

Keep tunnel credentials separate from the device's private key.

For example:

```text
device_tunnel_credentials
--------------------------------
id
device_id
encrypted_secret
created_at
revoked_at
```

The device private key remains on the Agent.

If Rathole can be integrated directly with the chosen device identity mechanism, the separate tunnel credential may be reduced or removed. That integration should be verified against the actual Rathole authentication and configuration model before relying on it.

## User/device binding

A device can be bound to users.

```text
device_users
--------------------------------
id
device_id
user_id
role
created_by
created_at
revoked_at
```

Example:

```text
PC1 ← Susi
role = admin

PC1 ← Ewax
role = guest
```

Binding a PC to an account can give that account administrative control, according to the application's policy.

## Invitations

A device owner can create an invitation for another user.

```text
invites
--------------------------------
id
device_id
created_by
code_hash
role
expires_at
used_at
used_by
created_at
```

The invitation code is a bearer credential.

Use:

- high-entropy random codes
- short expiration times
- HTTPS
- single-use semantics
- hashed codes in the database where practical
- no raw codes in application logs

Invitation consumption should be atomic.

Conceptually:

```sql
UPDATE invites
SET used_at = NOW(), used_by = ?
WHERE code_hash = ?
  AND used_at IS NULL
  AND expires_at > NOW();
```

Only the request that successfully consumes the invitation should create the user/device binding.

## Revocation

User/device access should support revocation independently of JWT expiration.

```text
device_users.revoked_at
```

A valid JWT does not automatically mean that the user still has access to every device.

Applications and devices should also support explicit status or revocation where appropriate.

## Session and tunnel lifecycle

The server should track active device connections.

When an Agent connects:

```text
1. Authenticate the device.
2. Associate the connection with device_id.
3. Replace or reject an older connection according to policy.
4. Mark the device online.
```

When the connection closes:

```text
1. Remove the active connection mapping.
2. Mark the device offline.
3. Reject requests that require the unavailable device.
```

Use heartbeats or equivalent stale-connection detection.

A `request_id` can correlate remote requests and responses.

## Database model

```text
users
--------------------------------
id
username
...

devices
--------------------------------
id
device_id
public_key
name
status
created_at
...

device_users
--------------------------------
id
device_id
user_id
role
created_by
created_at
revoked_at

invites
--------------------------------
id
device_id
created_by
code_hash
role
expires_at
used_at
used_by
created_at

device_apps
--------------------------------
id
device_id
name
type
config
created_at

device_tunnel_credentials
--------------------------------
id
device_id
encrypted_secret
created_at
revoked_at

audit_logs
--------------------------------
id
user_id
device_id
action
result
created_at
metadata
```

## Security boundaries

The architecture has several independent security boundaries.

```text
User
  ↓
User authentication
  ↓
Authorization
  ↓
Device
  ↓
Device authentication
  ↓
Tunnel / LAN transport
  ↓
Agent
  ↓
Registered application
```

A compromise of one layer should not automatically grant authority at another layer.

For example:

- Knowing a `device_id` does not prove device ownership.
- Decoding a JWT does not prove that it is valid.
- Being authorized for PC1 does not automatically authorize every application on PC1.
- Being able to discover a device does not cryptographically prove its identity.
- A tunnel credential does not represent a user's application permissions.
- A user's JWT does not prove that the request originates from the physical device.

## Pentest cases

### JWT

Test:

- modified payload
- changed `sub`
- changed role claims
- changed `exp`
- expired token
- invalid signature
- wrong signing algorithm
- wrong issuer
- wrong audience
- token replay
- missing required claims

The server must verify the signature and relevant claims.

### Device identity

Test:

- fake `device_id`
- device ID collision
- replayed device authentication
- stolen device credential
- invalid certificate
- revoked certificate
- unauthorized device registration
- duplicate active connections

### Local LAN

Test:

- unauthenticated LAN access
- malicious mDNS advertisements
- device impersonation
- rogue client
- traffic interception
- application enumeration
- access to unregistered applications

For the MVP, unauthenticated LAN access is expected. The security test should therefore verify that the exposed surface is limited to the intended applications and that the Agent cannot be used as an arbitrary proxy.

### Invitations

Test:

- reuse
- expiration
- brute force
- race conditions
- revoked device
- revoked creator
- privilege escalation through the invitation role
- leaking invitation codes through logs or URLs

### Remote tunnel

Test:

- invalid tunnel credential
- unauthorized device
- stale tunnel
- duplicate tunnel
- tunnel hijacking
- cross-device routing
- request ID confusion
- unauthorized application routing

## Recommended implementation stages

### MVP

Implement:

```text
Agent
├── device identity
├── LAN discovery
├── secure local connection
├── application registry
└── local application proxy

Client
└── LAN discovery + local access

Remote infrastructure
└── Rathole tunnel
```

For local access:

```text
No account
No JWT
No Internet
No central authorization
```

The MVP LAN boundary is simply:

```text
Can reach the Agent on the LAN
        ↓
Can use exposed applications
```

### V2

Add:

```text
User accounts
JWT authentication
Device/user bindings
Roles
Invitations
Application permissions
LAN user authentication
Revocation
Audit logs
```

The intended V2 model becomes:

```text
User
  ↓
Authentication
  ↓
Authorization
  ↓
Device
  ↓
Application
```

Remote access can use the central server for authentication and authorization, while local access can remain direct over the LAN.

## Target architecture

```text
                         Central server
                    ┌────────────────────┐
                    │                    │
                    │ User auth          │
                    │ Authorization      │
                    │ Device registry    │
                    │ Invitations        │
                    │ App permissions    │
                    │ Audit logs         │
                    │                    │
                    └─────────┬──────────┘
                              │
                         Rathole tunnel
                              │
                              │
                    ┌─────────▼──────────┐
                    │ PC1 Agent          │
                    │                    │
                    │ Device identity    │
                    │ LAN discovery      │
                    │ Local access       │
                    │ App routing        │
                    └───────┬────────────┘
                            │
                    ┌───────┴────────┐
                    │                │
                  dufs          Other apps


Local:

Client ── mDNS ──► PC1 Agent ──► Application

Remote:

Client ── HTTPS ──► Server ── Rathole ──► PC1 Agent ──► Application
```

The local path is independent of the Internet in the MVP. The remote path uses the central server and tunnel infrastructure. Version 2 can add LAN authorization at the Agent boundary while keeping the same device identity, application routing, and transport layers.
