# 46: is per-workspace rate shaping a small patch to `iroh-relay`?

**Verdict: go.** 209 production lines across 6 files, against iroh `v1.2.0`. The prototype is
in `hosted/relay/patches/0001-per-connection-rate-limit.patch` and #30 starts from working code.

One finding changes the design rather than the verdict: the bucket cannot be keyed by
workspace, only by connection. The cloud absorbs that, and the section below says how.

Two errors in r3 §11.3 turned up on the way. Both affect #24, and both are cheap now.

## The question

#30 assumed the endpoint id is known where the rate limiter is attached, so a rate could be
carried back from the authorize response and applied. The assumption came from reading the
config struct, never the accept path.

The assumption is wrong in its particulars and right in its conclusion. The endpoint id is
*not* known where the limiter is attached, and the patch is still small, because upstream
already built the mechanism that closes the gap.

## What upstream already does

More than #46 assumed, and this is most of why the patch is small.

`RateLimited` is not configured once at construction. It holds a
`watch::Receiver<Option<ClientRateLimit>>` and re-reads it on every read poll
(`server/streams.rs:565`), rebuilding its bucket when the value changes. Upstream added this
for `RelayService::set_client_rate_limit`, which retargets every live connection at once.

So live rate changes already work, and a tier upgrade already needs no reconnect. That is
one of #46's four questions answered with no patch at all, and it is covered by upstream's
own `streams::tests::test_ratelimiter_live_update`.

What upstream does not have is any way to give two connections different rates. There is one
`watch::Sender` on the whole service and every connection subscribes to it.

## The ordering problem

In `server/http_server.rs::accept`, the limiter is attached to the socket before the
handshake runs:

```
accept()
  RateLimited::from_watcher(io, ...)   <- limiter attached, endpoint id unknown
  handshake::serverside(...)           <- endpoint id learned here
  authorize_with(...)                  <- AccessControl::on_connect, the rate is decided here
  Clients::register(...)
```

The limiter has to exist first because it wraps the raw socket, and the endpoint id only
exists after the handshake it wraps. Restructuring that would be the invasive change #46 was
worried about.

The watch channel makes it unnecessary. Give each connection its own channel instead of a
subscription to the shared one, seed it with the server-wide value, and push the real rate in
after `authorize_with` returns. The limiter picks the change up on its next read poll, which
happens before any meaningful traffic has moved.

## The patch

| File | Added | What |
|---|---:|---|
| `server.rs` | 14 | `Access::AllowLimited { rate_limit }` |
| `protos/handshake.rs` | 11 | `authorize_with` returns the rate alongside the guard |
| `server/http_server.rs` | 26 | per-connection channel; push the rate after authorize |
| `server/client.rs` | 66 | hold the sender for the connection's life; override flag |
| `server/clients.rs` | 43 | `set_connection_rate_limit`, `connection_rate_limit` |
| `main.rs` | 49 | parse a JSON authorize response carrying `rate_bps` |
| | **209** | plus 135 lines of test |

`cargo fmt`, `cargo clippy --all-targets`, and all 83 upstream tests pass on the patched
tree. `hosted/relay/apply.sh` was run against a fresh clone to confirm the patch applies and the
result still passes.

### The one regression, and why the patch is bigger than it looks

Moving each connection onto its own channel silently broke
`RelayService::set_client_rate_limit`: connections already established stopped hearing
server-wide changes, because they no longer subscribed to the channel it writes to. Nothing
we ship uses that API, so nothing we ship would have noticed, and it would have surfaced as a
mystery on some future upgrade.

Restoring it is where roughly a third of the diff went. Each connection carries an
`AtomicBool` saying whether the access control gave it a rate of its own, and
`set_client_rate_limit` now fans out to the connections that did not get one. The resulting
rule, tested in `test_relay_per_connection_rate_limit`:

- Admitted with `Access::Allow`: follows the server-wide limit, before and after connecting.
- Admitted with `Access::AllowLimited`: pinned to its own rate, ignores server-wide changes.
- `set_connection_rate_limit(id, rate)` pins a live connection either way.

### Public API change

`Handshake::authorize_with` now returns `(OnDisconnectGuard, Option<ClientRateLimit>)`
instead of `OnDisconnectGuard`. Only embedders that mount the relay on their own HTTP server
call it, and upstream has one such test (`tests/relay_axum.rs`), updated in the patch.

## The workspace question

**A bucket cannot be keyed by workspace.** `RateLimited` owns its `Bucket` by value
(`server/streams.rs:335`) and is one per connection. Two devices in a workspace are two
connections, two buckets, and twice the rate. Sharing one bucket means `Arc<Mutex<Bucket>>`
and taking a lock on every read poll of every connection, which is a real change to the hot
path and a much larger argument than this patch.

It is also unnecessary. The cloud already decides the rate per connection at authorize time,
and it already knows how many devices a workspace has, because it is the thing that admits
them. So it divides:

```
rate returned to device d  =  workspace_rate / active_devices(workspace)
```

When the count changes, the cloud calls `set_connection_rate_limit` on the existing
connections and the change lands without a reconnect. The workspace aggregate is held by the
cloud's arithmetic rather than by a shared bucket in the relay.

What this costs: an idle device still holds its share, so a workspace with four devices where
one is transferring gets a quarter of its rate. Fixing that means feeding demand back to the
cloud so it can rebalance, which is a cloud change and not a relay change. It should be
deferred until real usage shows whether it matters. For capacity planning, which is the
reason the dial exists, the aggregate is what counts and the aggregate is correct.

**This belongs in #30 and #24 as a requirement.** Without the division, a workspace's real
ceiling is its rate times its device count, and VPS capacity planning is built on a number
that does not hold.

## Two corrections to r3 §11.3 and #24

Found by reading `main.rs`, and both would have cost an afternoon of debugging later.

**The header is `X-Iroh-NodeId`.** r3 §11.3, #24 and upstream's own doc comment
(`main.rs:170`) all say `X-Iroh-Endpoint-Id`. The constant that is actually sent says
otherwise:

```rust
const X_IROH_ENDPOINT_ID: &str = "X-Iroh-NodeId";   // main.rs:36
```

The name survived iroh's `NodeId` to `EndpointId` rename in the docs and not in the wire.
Our endpoint must read `X-Iroh-NodeId`, or read both.

**Stock upstream will not accept the JSON response r3 §11.3 specifies.** It requires the body
to be exactly the text `true`, and rejects anything else, JSON included:

```rust
Ok(text) if text == "true" => Ok(()),
Ok(_) => bail_any!("Invalid response text (must be 'true')"),
```

The patch adds the JSON form and keeps the bare `true` working:

```json
{"allow": true, "rate_bps": 8000000, "max_burst_bytes": 800000}
{"allow": false}
```

`rate_bps` omitted means the server-wide limit, which is the right default for a workspace
with no dial set.

## Receive side only

The bucket wraps `poll_read` and `poll_write` is untouched. r3 §11.3 already reasoned that
one receive-side bucket is enough, since a relay cannot emit a byte it did not first accept.
That reasoning is now load-bearing rather than a convenience: there is no send-side limit to
patch without writing one.

It holds for relayed traffic between our own endpoints, which is all a relay carries. It is
worth stating in the threat model (#47) as an assumption rather than leaving it implicit.

## What is still untested

The prototype is proven by unit and integration tests, not by bytes over a wire.

- Shaping accuracy under real load. Upstream's `test_ratelimiter` asserts the bucket hits its
  configured rate within rounding on a simulated clock. No throughput was measured against a
  deployed relay, because there is no relay yet (#28).
- Behaviour at low rates, where the 100 ms refill period may be coarse relative to the frame
  size, and burst settings start to matter.
- Cost of the fan-out in `set_client_rate_limit` with many connections. It is a `DashMap`
  iteration doing an atomic load per connection, so it should be cheap. Not measured.

None of these change the verdict. They belong in #30 as acceptance criteria.

## Upgrade risk

The patch touches five functions. Three are structural and unlikely to move
(`Access`, `authorize_with`, `Config`). Two are the risk:

- `http_server.rs::accept` is where three separate insertions land. Any upstream change to
  the order of handshake and authorization will conflict, and the conflict is the point:
  the patch's correctness depends on that order.
- `set_client_rate_limit` will conflict if upstream reworks its own rate-limit plumbing,
  which is plausible, since per-connection rates are an obvious feature for them to add.

If upstream does add per-connection rates, this patch is deleted and `hosted/relay/patches/` goes
back to empty. Worth watching rather than acting on.

## What changes in the plan

- **#30** starts from `hosted/relay/patches/0001-per-connection-rate-limit.patch`. Its remaining
  work is deployment, measurement against a real relay, and the untested items above.
- **#24** must read `X-Iroh-NodeId`, return the JSON shape above, and divide the workspace
  rate by active device count.
- **#28** must deploy the patched binary, not the stock one, and `hosted/relay/apply.sh` is how it
  is built.
- **#47** should record two assumptions: shaping is receive-side only, and a workspace's
  aggregate rate is enforced by cloud arithmetic rather than by the relay.
