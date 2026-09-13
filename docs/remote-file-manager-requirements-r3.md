# Remote File Manager — Product & Architecture Requirements (Revision 3)

Revision 3 is a rewrite, not a patch. It keeps the business idea, removes everything the chosen architecture does not need, and records the feasibility research that drove the choices. Where a decision is a trade-off rather than a fact, it is marked so you can overturn it.

**Revision 3.1 — September 2026.** Three changes, all narrowing:

1. **Android is deferred to v1.1.** v1 is desktop only. This removes the plan's largest risk (old §20.1) and, with it, SAF share roots, `ACCESS_LOCAL_NETWORK`, the service picker, and the foreground-service requirement. Nothing below the UI changes, so Android returns as an additional agent against an unchanged core.
2. **Old §20.2 — relay authorization — is resolved, not open.** `iroh-relay` takes an `access` mode in its TOML config, and one of those modes delegates to an HTTP endpoint of ours. No authenticating proxy is needed. See §11.3.
3. **The control plane is Go**, as D4 already permitted. The only Rust written is `rfm-core` and the desktop agent; the relay is a deployed binary.

---

## 1. The idea

Users own their files on their own devices. They should be able to browse and transfer those files between their devices over the LAN with nothing more than the app installed, and — if they pay — from anywhere, without moving the files to a cloud.

The business is: **free local product, paid remote connectivity.** The cloud sells reachability, not storage.

---

## 2. Scope

**In scope (v1)**

* Desktop agent (Windows, macOS, Linux) with a built-in file manager UI.
* Local workspace of trusted devices, created and managed without an account.
* Browse, download, upload, delete, move, rename files on any workspace device, from any workspace device.
* Optional remote access via a paid account: direct connection where possible, encrypted relay otherwise.
* Cloud membership so other accounts' devices can be added to a workspace.
* Transfer of cloud ownership between accounts.

**Out of scope (v1)**

* Cloud file storage, sync, backup, versioning.
* Browser-based file access at `https://dash.example.com`. The web app manages accounts, billing, and membership only. (Decision D2 explains why.)
* The Android agent. Deferred to v1.1 — see Revision 3.1. The concepts, the trust list, the access rule and the file protocol are all written to accommodate it unchanged.
* iOS.
* Shared/public links, guest access.
* Per-file permissions. Permissions are per shared folder.

---

## 3. Concepts

| Term | Meaning |
|---|---|
| **Device** | A machine running the agent. Identified by an Ed25519 key pair generated on first run. The public key is the device ID. |
| **Workspace** | A set of trusted devices. Identified by a random 128-bit workspace ID minted by the creating agent. |
| **Trust list** | The signed, versioned document listing the devices in a workspace, their roles, and optional account bindings. The workspace *is* its trust list. |
| **Admin device** | A device whose trust-list role is `admin`. Only admin devices can change the trust list. The creating device is the first admin. |
| **Share** | A folder a device exposes to the workspace, with a permission (`read` or `read-write`) per grantee. |
| **Account** | A user identity in the cloud. Needed only for remote access. |
| **Cloud association** | The link between a workspace and exactly one account (the cloud owner). Optional. |
| **Cloud owner** | The account whose subscription pays for the workspace's remote access, and who manages membership. |
| **Member** | An account the owner has admitted. Membership lets that person's devices *request* to join; it does not grant file access. |
| **Account-bound device** | A device whose trust-list entry records which account paired it. Removing the member removes the device. |

Five identities, all independent: device ID, workspace ID, account ID, trust-list version, share ID. None derives from another. Renaming, transferring, or disconnecting never changes any of them.

---

## 4. Principles

1. Local works with no account, no subscription, no Internet.
2. Remote access is a capability added to the same workspace, never a second workspace.
3. **The agent decides every access request.** The cloud can only make a remote request possible or impossible; it can never make one allowed.
4. Only admin devices sign the trust list. The cloud never adds a device.
5. Discovery is not authentication. Authentication is not authorization.
6. The relay sees ciphertext only.
7. Nothing the user sees depends on which network path is in use.

---

## 5. Feasibility research

What was checked, what was found, and what it changed. Checked September 2026.

| Question | Finding | Consequence |
|---|---|---|
| Can an HTTPS web app talk to a LAN agent over plain HTTP? | Chrome 142+ (Oct 2025) gates local-network requests behind a "Local Network Access" permission prompt and, if granted, relaxes mixed-content blocking for those requests. Firefox and Safari have no equivalent; the request is simply blocked. Requests from insecure (HTTP) origins are rejected. | Chromium-only, prompt-gated. Not acceptable as the primary path. Drives **D2**. |
| Can a browser reach LAN peers by WebRTC? | Yes, in all major browsers, but it requires a signaling channel and adds ICE/STUN/TURN/DTLS/SCTP. TURN relays are stateful and comparatively expensive to run. | Only worth carrying if browser file access is a requirement. It is not in v1. Drives **D2**. |
| Does `http://something.local` work on Android browsers? | Android 12+ resolves `.local` in browsers via the system resolver, but it sends *legacy unicast* mDNS queries (RFC 6762 §6.7). Responders that only reply by multicast are not seen, and OEM behaviour varies. | A workspace hostname would need a custom mDNS responder and still be unreliable. Dropped. Drives **D3**. |
| Android LAN access restrictions | Android 16 introduced an opt-in `ACCESS_LOCAL_NETWORK` runtime permission. Android 17 enforces it for apps targeting SDK 37+: all LAN sockets and mDNS/NSD need it. A system picker (`FLAG_SHOW_PICKER`) grants access to one selected service without the broad permission. | A workspace talks to many devices, so the broad permission is required. The picker is a fallback for users who deny it. See §13.2. |
| Is there a transport library that does identity, LAN, hole-punching, and relay together? | iroh 1.0 (June 2026): QUIC/TLS 1.3, dial by Ed25519 key, direct LAN and hole-punched connections with stateless relay fallback, ~90–99 % direct-connection rate reported, self-hostable relays, first-party Rust with Swift/Kotlin/Python/JS bindings. Limitations: no browser support (QUIC only); the published Kotlin artifact is JVM-only and Android needs a from-source or third-party JNI build; Android backgrounding tears down endpoints. | Adopted as the transport. Removes the custom WebRTC stack, custom relay, custom signaling, and custom handshake from the previous draft. Drives **D1**. |
| Can a self-hosted relay authorize per device key? | `iroh-relay` accepts an `access` mode in its TOML config: `Everyone`, `allowlist` / `denylist` of endpoint ids, `shared_token`, or **`HTTP`** — which POSTs to a URL of ours with an `X-Iroh-Endpoint-Id` header and admits on a `200` carrying `true`. TLS with Let's Encrypt, per-client token-bucket rate limiting, and Prometheus on `:9090` are all config. | Relay authorization is a config field, not a component. Closes the old §20.2 risk and collapses §10.3, §10.4, §10.5 and §11.3 into one endpoint. See **§11.3**. |
| libp2p as the alternative | Broader feature set, browser transports available, but wider configuration surface and hole-punching success reported around 70 %. | Rejected for v1; noted in §19. |
| Tailscale / WireGuard mesh as the alternative | Solves the same connectivity problem but presumes a control server and coordination model that overlaps with ours, and pulls a full L3 VPN into an app that only needs one stream type. | Rejected; noted in §19. |

Sources: Chrome for Developers "New permission prompt for Local Network Access"; web-platform-dx feature page for `local-network-access`; Android Developers "Local network permission" and `NsdManager` reference; iroh docs FAQ, Kotlin guide, and platform matrix; ARK Builders and n0 comparisons of WebRTC/libp2p/iroh; keepsimple1/mdns-sd PR #469 on Android legacy-unicast resolution.

---

## 6. Architecture decisions

### D1 — One transport library: iroh (trade-off)

All device-to-device traffic runs over iroh QUIC connections. iroh supplies: the device key pair, mutual authentication by key, end-to-end TLS 1.3, LAN direct paths, NAT hole-punching, relay fallback, and migration between paths. The product adds one ALPN (`rfm/1`) and the file protocol on top.

What this deletes from the requirements: WebRTC, ICE, STUN, TURN, a signaling service, a custom relay, a custom Noise handshake, application-level rate shaping, and transport-migration logic. The cloud no longer signals anything.

Risks accepted: Rust is mandatory for `rfm-core` and the desktop agent (see D4); the library is at 1.0 and the team is small. The Android binding risk is deferred with Android itself (Revision 3.1). Mitigation: the file protocol is defined over an abstract bidirectional stream so the transport can be swapped (§19).

### D2 — File UI lives only inside the agents (trade-off)

The file manager UI is a web bundle rendered inside the desktop agent's window (and, in v1.1, the Android agent's WebView). It talks to its own agent in-process. It never makes network calls itself.

`https://dash.example.com` is a conventional web app for sign-in, billing, workspace list, members, devices, and ownership transfer. It shows no files.

Why: every browser-based LAN or P2P path (§5) is either Chromium-only, prompt-gated, or requires the WebRTC stack that D1 removed. Browser file access can be added later as a separate project without changing anything below the UI.

### D3 — No workspace hostname (fact-driven)

Discovery is DNS-SD (`_rfm._tcp`) with the workspace ID in a TXT record. Devices find each other by ID, never by name. There is no `myhome.local`, no host election, no collision handling. Workspace names are labels for humans only.

### D4 — One shared core (trade-off)

The agent is a Rust library (`rfm-core`) containing iroh, the trust list, shares, the file protocol, and the access-decision rule. Desktop wraps it with Tauri (or equivalent) for the window.

The cloud is a separate service in whatever stack the team prefers; it only speaks HTTPS+JSON and operates iroh relays. **It is Go.** The coupling is two things and no more: an HTTPS+JSON API for the agents, and the one endpoint the relay calls to authorize a device key (§11.3). The cloud never speaks iroh's protocol and never links `rfm-core`.

The relay is the upstream `iroh-relay` binary, deployed and configured. It is not code we write.

So the Rust surface is `rfm-core` plus the desktop shell. Android, when it returns in v1.1, wraps the same library with UniFFI/JNI.

### D5 — The cloud is a directory, a billing system, and a relay operator. Nothing else.

It stores accounts, subscriptions, associations, members, and the public keys of devices it has been told about. It hosts relays and tells them which device keys may use them. It cannot read files, add devices, or sign anything the agents trust.

---

## 7. Components

```text
┌────────────────────────────────────────────────────────┐
│ Agent (desktop; Android in v1.1)                       │
│                                                        │
│  UI bundle ──in-process──▶ rfm-core                    │
│                             ├── trust list + shares    │
│                             ├── access decision (§9)   │
│                             ├── file protocol          │
│                             └── iroh endpoint ─────────┼──▶ other agents
│                                                        │      (LAN / direct / relay)
└────────────────────────────────────────────────────────┘
                 │ HTTPS (only when remote access is on)
                 ▼
┌────────────────────────────────────────────────────────┐
│ Cloud                                                  │
│  accounts · subscriptions · associations · members     │
│  device directory (public keys) · relay auth · audit   │
│  iroh relays (data plane, ciphertext only)             │
└────────────────────────────────────────────────────────┘
```

Boundaries:

* The UI bundle has no network access and no filesystem access. It calls `rfm-core` through an in-process API.
* `rfm-core` is the only thing that reads the disk and the only thing that opens sockets.
* The cloud never receives a file byte, a trust-list signature, or a filesystem path.
* Relays never receive plaintext.

---

## 8. Identity and trust

### 8.1 Trust list

```text
TrustList
├── workspace_id
├── name
├── version            monotonic integer
├── entries[]
│   ├── device_key     Ed25519 public key (= iroh endpoint ID)
│   ├── display_name
│   ├── role           admin | standard
│   ├── account_id     optional
│   └── status         active | revoked
└── signature          by one admin device key, over everything above
```

A device accepts version N+k only if it is signed by a key that was `active` and `admin` in the version it currently holds. The first version is self-signed by the creating device. There is no other root of trust.

Every device holds the full list. It is persisted with atomic writes.

### 8.2 Local pairing

No account, no Internet.

1. Joining device shows a QR code / 8-character code containing its device key and a one-time secret.
2. An admin device scans or types it, sees the key fingerprint and proposed name, and approves.
3. The admin device signs version N+1 adding the key as `standard` with no account binding, and sends it to the new device over a direct iroh connection authenticated with the one-time secret.

A standard device cannot admit anyone.

### 8.3 Remote pairing

Requires: the joining device is signed in to an account that is a member of the workspace, and the owner's subscription is active.

1. Joining device submits `{workspace_id, device_key, name}` to the cloud.
2. Cloud checks membership and subscription, stores the pending request, and notifies the workspace's admin devices.
3. An admin device approves; the UI shows the requesting account, device name, and key fingerprint. The admin signs version N+1 with the key as `standard`, bound to the requesting account.
4. The admin pushes the new version to the cloud; the cloud delivers it to the new device and authorizes that key on its relays.

Admins may set "auto-approve devices from these members" per member. The signature still comes from an admin device; auto-approve only removes the tap.

### 8.4 Propagation

A new trust-list version reaches other devices by: the DNS-SD TXT record advertising `v=N` (peers fetch from whoever is higher), the cloud pushing to every reachable device when associated, and the version exchange at the start of every connection.

### 8.5 Revocation

An admin marks an entry `revoked` in a new version. Every device that receives it rejects the revoked key at connection time. The revoked device need not be reachable; revocation is enforced by the peers it tries to talk to. If associated, the cloud also de-authorizes the key on relays.

### 8.6 Key rotation

Routine: device generates a new key, signs a rotation record with the old one, an admin countersigns a version that replaces the key. Offered yearly.

Compromise: revoke, re-pair with a fresh key. There is no rotate-under-compromise.

### 8.7 Admin succession and loss

* Promoting a device to admin requires the promoted device to co-sign acceptance.
* The last admin cannot be revoked or demoted; the UI refuses.
* Once a second desktop joins, the UI recommends making it an admin.
* **All admins lost:** the trust list is frozen. Existing devices keep working with each other indefinitely; nothing can be added or removed. Two exits: (a) an opt-in encrypted **recovery bundle** (admin private key wrapped by a passphrase, exported at setup and after promotions, never sent to the cloud) imported on another workspace device; (b) create a new workspace from any remaining device and re-pair. Files never move in either case.

---

## 9. Access decision rule

Evaluated by the agent that owns the files, for every request, and nowhere else.

```text
1. Connection is an iroh connection whose peer key is K.
2. K is active in my trust list for workspace W                 else DENY
3. Peer's trust-list version ≥ mine − 1                          else send update, retry
4. Path is relayed?
      no  → continue
      yes → I hold a relay-authorization token for (W, K) < 24 h old
            or this session was authorized at open                else DENY
5. Requested path is inside one of my shares S                   else DENY
6. S grants K, or K's bound account, or "workspace"
   the requested permission                                       else DENY
7. ALLOW
```

Step 2 is the only step that can grant. Steps 4 can only deny, and only for relayed paths; direct paths (LAN or hole-punched) are never affected by cloud state. Admin role grants no file access.

Path checks in step 5 are performed on the canonicalized, symlink-resolved path and must stay inside the share root.

---

## 10. Cloud control plane

### 10.1 Enable Remote Access

From an admin device only. The device signs `{workspace_id, trust_list_version, device_key, nonce}` and submits it with the user's session. The cloud requires an active subscription on that account and enforces `UNIQUE(workspace_id)` on associations: the first valid request wins, later ones get "already connected to <account>". The cloud stores the current trust-list version and every active device key, and authorizes those keys on its relays.

Nothing on the agents changes. No re-pairing.

### 10.2 Ownership transfer

1. Owner initiates, names the target account.
2. Target accepts (must be able to hold an active subscription).
3. **One admin device confirms in-app** within 7 days, otherwise the request expires.
4. Cloud swaps `owner_account_id`, writes an audit record, demotes the previous owner to member.

Step 3 exists so that the cloud cannot hand remote control of someone's files to a third party without the file-holder's device agreeing. Trust list, shares, device keys: unchanged.

### 10.3 Membership

Roles: `owner` (one), `manager` (may add/remove members and approve pairing on the cloud side), `member`. None of them can touch the trust list or shares.

Removing a member: the cloud de-authorizes their bound device keys on relays immediately, and sends a revocation instruction to admin devices; the next admin online signs the revocation automatically. Until then, those devices can still connect over direct paths. The UI shows "removal pending on N devices". Unbound devices (locally paired without an account) are untouched.

### 10.4 Disable Remote Access

Either the owner (cloud-side) or any admin device (locally, effective even if the cloud is unreachable) can do it alone. The association becomes `disabled`; relays stop accepting the workspace's keys; agents refuse relayed connections. Trust list, shares, files, LAN operation: unchanged.

Re-enabling by the same owner within 90 days restores membership. Otherwise membership starts empty.

### 10.5 Subscription

The owner's subscription covers the whole workspace. Members pay nothing.

Lapse: 14-day grace with remote access on → `suspended` (relays refuse, membership retained, local unaffected) → after 90 days suspended, association deleted, workspace is local-only.

Agents cache subscription state for 24 hours and label it stale after that.

### 10.6 Data model (cloud)

```text
accounts(id, email, …)
subscriptions(account_id, tier, status, current_period_end)
workspace_associations(workspace_id UNIQUE, owner_account_id, status, trust_list_version, created_at)
workspace_members(workspace_id, account_id, role, status)
devices(device_key PK, account_id NULL, display_name)
device_authorizations(workspace_id, device_key, status)      -- mirror for relay auth, never authoritative
pairing_requests(id, workspace_id, device_key, account_id, status, expires_at)
transfer_requests(id, workspace_id, from_account, to_account, status, expires_at)
audit_events(id, workspace_id, actor, action, at)
```

PostgreSQL. No Redis in v1; nothing here needs it.

---

## 11. Connectivity

### 11.1 Discovery

Every agent advertises `_rfm._tcp` with TXT `ws=<workspace id>`, `dev=<device key>`, `v=<trust-list version>`, and browses for the same. Match on `ws`. A device in several workspaces advertises one record per workspace.

When associated, the cloud also returns the endpoint addresses of workspace devices, so remote devices can be dialed without LAN discovery. iroh's own address lookup is disabled; the cloud directory replaces it so that no third-party DNS/DHT is involved.

### 11.2 Paths

iroh picks LAN direct, hole-punched direct, or relay, and upgrades relayed connections to direct when it can. The file layer sees one QUIC connection. The UI shows a small "direct / relayed" indicator and nothing more.

### 11.3 Relay

Self-hosted `iroh-relay`, one or more per region. It is the upstream binary, deployed and configured — not code we write.

**Authorization is a config field.** The relay's `access` mode delegates to an endpoint of ours:

```toml
[tls]
hostname   = ["relay.example.com"]
cert_mode  = "LetsEncrypt"
contact    = "ops@example.com"

[access]                                      # Everyone | allowlist | denylist | shared_token | HTTP
url          = "https://cloud.example.com/relay/authorize"
bearer_token = "…"                            # or IROH_RELAY_HTTP_BEARER_TOKEN

[limits.client.rx]                            # per-client token bucket, DoS protection only
bytes_per_second = 4_000_000
max_burst_bytes  = 8_000_000
```

The relay POSTs to `url` with an `X-Iroh-Endpoint-Id` header carrying the hex endpoint id, and admits the connection on a `200` whose body is `true`. That single endpoint answers for all four ways a key can lose relay access:

| Reason the answer is `false` | Requirement |
|---|---|
| The member was removed and this key is bound to them | §10.3 |
| Remote access was disabled | §10.4 |
| The subscription lapsed past grace | §10.5 |
| *(no quota row — relay use is shaped, not capped; see below)* | |

Plus the standing conditions: active association, key active in the trust-list mirror. The endpoint must be fast and cached — it is on the connection path — and it must **fail closed**.

#### Bandwidth is shaped per workspace, not capped per month

There is **no monthly byte quota.** Relay use is continuous and rate-limited: a workspace's tier buys
**one relay rate**, and the operator can change it for any workspace at any time. Nobody is cut off
for using the relay too much; they are served at the rate they pay for.

One rate covers both directions. A relay cannot emit a byte it did not first accept, so limiting how
fast it *accepts* from a workspace bounds that workspace's traffic in both directions. This is also
the direction upstream already limits (`limits.client_rx`), which keeps the patch below small.

Three mechanisms, and they are deliberately small:

**Placement — one workspace, one home relay.** An iroh endpoint pings the relays in the relay map it
was given and adopts the lowest-latency one as its home relay. The cloud hands out that map, so
naming a single relay in it *is* placement. Assigning every device of a workspace to the same relay
means a workspace never spans relays — which removes the need for any cross-relay bandwidth
coordination.

**Shaping — one token bucket per workspace, local to its relay.** The relay already learns the
endpoint id at accept, because that is what it sends to the authorize endpoint. So the authorize
response carries the policy as well as the decision:

```text
POST /relay/authorize      X-Iroh-Endpoint-Id: <hex>
  -> 200  {"allow": true, "rate_bps": 8000000, "workspace": "<id>"}
  -> 200  {"allow": false}
```

Endpoints sharing a workspace share that workspace's bucket, so the rate divides between a
workspace's machines by demand rather than being handed out per device.

**Capacity — committed rate is the sizing input.** How many relays to keep online is the sum of
committed rates placed on each relay against its measured capacity, not a guess from connection
counts. Placement is then a packing decision the cloud makes when it hands out a relay map.

Committed rate is what was *sold*, and workspaces are idle most of the time, so provisioning for all
of it at once would be waste. The gap is an **overcommit ratio, and it is an operator setting with a
conservative default — not a number this document picks.** It cannot be chosen correctly before real
usage exists. What v1 must do is record the measurements that let it be chosen later: committed rate
and actual throughput per relay over time, and peak concurrent relayed throughput per workspace.

**What this costs.** Upstream `iroh-relay` applies one service-wide receive-side rate to every
client (`limits.client_rx`), with no per-endpoint variation. Per-workspace shaping therefore requires
a patch: carry the rate from the authorize response onto the connection and attach a bucket keyed by
workspace. It is bounded and well-located — the endpoint id is already in hand at that point, and
the direction is the one upstream already limits — but it is a fork, and a carry cost on every
`iroh-relay` upgrade.

Direct traffic is never shaped and never counted. Only relayed bytes traverse a relay, so
attribution needs no path telemetry.

Bytes are still **counted** per workspace, for the operator's capacity planning and for the owner's
own visibility. Counting is not billing and does not gate access.

Relays are stateless and scale horizontally on connection count and bandwidth.

### 11.4 Sessions and transfers

Transfers are chunked (4 MiB), each chunk hashed (BLAKE3), with a transfer ID persisted on both ends. A transfer resumes from the last acknowledged chunk after any reconnect within 24 hours. The UI shows "reconnecting" for up to 30 s before offering retry. Directory listings are not cached across reconnects.

---

## 12. Filesystem layer and shares

Operations: `list`, `stat`, `read(range)`, `write(range)`, `delete`, `move`, `mkdir`. Streaming, cancellable, structured errors.

```text
Share
├── id
├── device_key         the device that owns the folder
├── root               absolute path
├── label
└── grants[]
    ├── grantee        workspace | device:<key> | account:<id>
    └── permission     read | read-write
```

Shares are configured on the owning device only. Share definitions (not contents) are advertised to the workspace so other devices can list what exists. New devices see nothing until a grant includes them.

Enforcement is by the owning agent (§9). The UI's greying-out of buttons is cosmetic.

---

## 13. Platforms

### 13.1 Desktop

Installer installs the agent as a user-level service plus a windowed app. First run creates a workspace (or joins one by code). Key stored in the OS keystore (Keychain / DPAPI / Secret Service). mDNS via the platform responder where present (Bonjour, Avahi) or an embedded responder otherwise.

### 13.2 Android — deferred to v1.1

Not built in v1 (Revision 3.1). Recorded here because the v1 design must not foreclose it, and because the platform constraints shape what the core may assume.

* Kotlin app embedding `rfm-core` through UniFFI/JNI. The published iroh Kotlin artifact is JVM-only; Android needs a from-source or third-party JNI build. **This was the plan's single biggest risk and deferring Android is what removes it.**
* Target SDK 37+: declare and request `ACCESS_LOCAL_NETWORK`. Explain before prompting. If denied, offer the system service picker (`NsdManager` with `FLAG_SHOW_PICKER`) to connect to one chosen device at a time, and remote access if enabled.
* Folder access via Storage Access Framework; persist tree URIs with `takePersistableUriPermission`. Each chosen tree is a share root. **This is the one place Android changes a v1 type** — §12's `root` is an absolute path today and must widen to accommodate a tree URI. Keep it opaque to everything above the filesystem layer so that widening is local.
* Accepting incoming connections requires a foreground service ("Workspace active" notification) while the user has sharing on. Without it the OS suspends the endpoint.
* Key in Android Keystore.
* Android devices can be admins.

---

## 14. Lifecycle

```text
create (admin device)
   │
   ▼
local-only ◀──────────────────────────────────────────┐
   │ enable remote access (admin device + owner acct) │ disable (owner OR admin device)
   ▼                                                  │
cloud-associated ──── transfer ────▶ cloud-associated │
   │                                 (new owner)      │
   │ subscription lapse                               │
   ▼                                                  │
suspended ──── renew ──▶ cloud-associated             │
   │ 90 days                                          │
   └──────────────────────────────────────────────────┘

delete workspace (admin device): final signed version with status=deleted;
every agent drops its copy on receipt; cloud deletes association and members;
files and device keys untouched.

account deletion: owned workspaces become local-only; memberships removed (§10.3).
```

The workspace ID is constant in every state but deleted.

---

## 15. Authority matrix

| Operation | Admin device | Cloud owner | Manager | Member |
|---|---|---|---|---|
| Create / delete workspace | ✔ | — | — | — |
| Add, revoke, promote, bind device | ✔ | — | — | — |
| Configure own device's shares | ✔ (any device's user, on that device) | — | — | — |
| Enable remote access | initiates | must be the signed-in account | — | — |
| Disable remote access | ✔ alone | ✔ alone | — | — |
| Transfer ownership | confirms | initiates | — | — |
| Manage members | — | ✔ | members only | — |
| Approve pairing (cloud side) | — | ✔ | ✔ | — |
| Sign trust list | ✔ | — | — | — |
| Request pairing for own device | — | ✔ | ✔ | ✔ |
| Subscription | — | ✔ | — | — |

---

## 16. UI terminology

| Say | Never say |
|---|---|
| Workspace | Local workspace / Cloud workspace |
| Local access · Remote access | Cloud mode |
| Enable Remote Access | Create cloud workspace · Convert to cloud |
| Cloud owner | Owner (ambiguous with admin device) |
| Admin device | Primary device · Host |
| Transfer Ownership | Change account |
| Disable Remote Access | Disconnect workspace |
| Remote access suspended | Expired workspace |
| Remove device · Remove member | Delete |
| Share (folder) | Root · Mount |

Status block on every workspace screen:

```text
My Home
Local access      Available on this network
Remote access     Off                          [ Enable Remote Access ]
                  / On · Alice pays            [ Manage ]
                  / Suspended · renew          [ Renew ]
```

---

## 17. Failure behaviour

| Condition | Behaviour |
|---|---|
| No Internet | Everything local works. Cloud screens show cached data marked stale. Relayed sessions drop; direct ones continue. |
| Cloud down | As above. Pending pairing/transfer/membership changes wait. Trust-list propagation continues over LAN. |
| Relay down | Direct paths unaffected. Relayed sessions retry other relays, then surface "reconnecting". |
| Agent restart | Trust list, shares, and in-flight transfer state reload from disk; transfers resume. |
| Network change | iroh re-establishes paths; file layer resumes chunks. |
| Revoked device offline | Rejected by every peer when it returns. |

No failure ever deletes workspace state, device keys, or files.

---

## 18. Observability

Agents (opt-in telemetry): connection outcomes by path type, transfer throughput and failures, resumed transfers, trust-list version skew, pairing outcomes.

Cloud: active associations, active members, relay authorizations, relay bytes per workspace, pairing and transfer request outcomes, subscription state transitions, audit log of every privileged action.

Relays: committed rate, actual throughput and headroom per relay, recommended relay count, and authorization denials by reason.

**Prometheus + Alertmanager + Grafana**, for the cloud and the relays. `iroh-relay` exposes Prometheus natively, so this is the scrape and storage layer either way.

The division of labour, because it decides how much UI gets built:

* **Grafana** owns time series, graphs, and exploration. Its panels are embedded in the operator console rather than reimplemented there.
* **Alertmanager** owns routing, grouping, silences, and escalation. None of that is rebuilt.
* **The operator console** owns live, domain-shaped health — is the fleet big enough right now, which relays are unhealthy, which workspaces sit on them, and why a given workspace's relay access is denied. Plus every dial in the system.

Native charts in the console are a later decision, taken against real usage, not a v1 one.

---

## 19. Alternatives considered

| Alternative | Why not (v1) | When to revisit |
|---|---|---|
| WebRTC + TURN + custom signaling | Two to three extra services, stateful relays, browser-only benefit that v1 doesn't need. | If browser file access becomes a requirement. It can be added as a second transport behind the same stream abstraction. |
| libp2p | Wider surface, lower reported hole-punch success, more configuration to get wrong. | If a public DHT or browser transports are needed. |
| Tailscale / Headscale / WireGuard mesh | Full L3 VPN and a second control plane for a product that needs one authenticated stream. Licensing and account coupling. | If users demand SMB/NFS-style mounting rather than an in-app browser. |
| Custom relay with a fleet coordinator | Per-workspace shaping is a requirement (§11.3), and upstream `iroh-relay` cannot express it, so a patch is needed either way. What a coordinator would add on top — leases, placement negotiation, and a cross-relay allowance loop — is avoided instead by pinning a workspace to one home relay, which makes its bucket local. | If one workspace ever needs more bandwidth than a single relay can serve, or if placement must rebalance while sessions are live. |
| Workspace `.local` hostname | Browser resolution is unreliable on Android, name collisions, host election; only useful for a browser entry point v1 doesn't have. | With browser file access. |
| Redis for ephemeral state | Nothing in v1 is shared across cloud instances that PostgreSQL can't hold. | If relay-auth lookups become a bottleneck. |

---

## 20. Risks to verify before committing

1. **iroh path telemetry.** Confirm the API exposes direct-vs-relayed per connection, and signals the change when a relayed connection upgrades mid-session. §9 step 4 needs it to decide, and §11.4 needs it to move a transfer to a better path.
2. **Desktop mDNS coverage.** Avahi is not always installed on Linux; the embedded responder must be tested on Windows without Bonjour. Include the unplugged-router test — principle 1 should be verified, not assumed.
3. **Key store portability.** Confirm the OS keystores allow non-exportable Ed25519 keys usable by `rfm-core` on all three desktop OSes, or fall back to an encrypted file keyed by the OS keystore. Note the likely tension: iroh signs with the key in process, so genuinely non-exportable may be unavailable.
4. **Desktop shell.** Confirm Tauri can host `rfm-core` in-process alongside the accept loop, install as a user-level service plus a windowed app on all three OSes, and stream a multi-GB file to the webview without buffering it.

**Closed since Revision 3** (see Revision 3.1):

* ~~iroh on Android~~ — deferred with Android itself. Was the single biggest risk in the plan.
* ~~Android 17 permission denial rate~~ — deferred with Android.
* ~~iroh relay authorization hook~~ — **resolved.** It is an `access` mode in the relay's config, with an HTTP delegation option. No authenticating proxy needed. §11.3 has the configuration.

---

## 21. Central principle

> A workspace is a signed list of device keys. Files stay on devices. The cloud can sell you a path to your devices from outside, and can introduce other people's devices to your admin devices, but it can never open a file, add a device, or take a workspace away.
