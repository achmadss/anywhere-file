# spike #2: OS keystore for the Ed25519 device key

Throwaway. Its own workspace, so `cargo build --workspace` at the repo root does not pull
iroh in. Findings are in `docs/spikes/keystore.md`.

```
cargo run --release -- store   # generate, write the seed to the keystore two ways
cargo run --release -- load    # separate process: reload both, iroh handshake with the key
cargo run --release -- wipe    # delete both keystore items and the sealed file
```

Only the macOS path was run. `store` writes two generic passwords into the login keychain
under the service `dev.anywherefile.spike-keystore`; `wipe` removes them.

The Linux numbers in the report came from a separate throwaway in a `rust:1-slim` container,
not from this crate. It is six lines and reproduced in full in the report.
