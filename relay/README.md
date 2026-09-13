# relay

The relay is `iroh-relay`, upstream's binary, configured rather than written (r3 §11.3) —
with one exception. Upstream's `limits.client_rx` is a single service-wide, receive-side
rate with no per-endpoint variation. Per-workspace bandwidth shaping is a product
requirement, so it needs a patch. That patch is what lives here.

**Whether the patch is small is not yet known.** #46 is the spike that reads the
connection-accept path and answers it. Until it reports, `patches/` is empty and this
directory builds stock `iroh-relay`. Do not start #30 before then.

## What is versioned

| | |
|---|---|
| `IROH_VERSION` | the upstream tag we build against |
| `patches/*.patch` | our diff, applied in filename order |
| `src/` | **not versioned.** Upstream's tree, fetched on demand, gitignored. |

Carrying a diff rather than a fork is deliberate: it keeps the size of what we own visible
in `git diff --stat`, and it makes an upgrade a merge conflict rather than an archaeology
project. If the diff ever grows past roughly a file or two, that is the signal to stop
patching and reconsider — either upstream the change or vendor properly.

## Build

```sh
relay/apply.sh                                  # fetch pinned iroh + apply patches
cargo build --release --manifest-path relay/src/Cargo.toml -p iroh-relay
```

## Upgrading iroh

1. Bump `IROH_VERSION`.
2. `rm -rf relay/src && relay/apply.sh`.
3. If a patch conflicts, `apply.sh` stops with the reject. Fix it in `relay/src`, then
   regenerate: `git -C relay/src diff > relay/patches/NNNN-name.patch`.
4. Re-run the shaping test from #30. A patch that still applies cleanly is not proof it
   still does the same thing — upstream can move the code the hook hangs off without
   touching the lines the patch names.
5. Commit `IROH_VERSION` and the regenerated patches together.

CI runs step 2 on any change under `relay/`, so a patch that has stopped applying is caught
on the PR that breaks it rather than at deploy time.

## Deployment

Not here. #28 owns provisioning and TOML config, #24 owns the authorization endpoint the
relay probes.
