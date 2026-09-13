//! The desktop agent.
//!
//! A plain binary for now. The Tauri shell lands in #4, the tray and lifecycle in #33–#34.
//! `rfm-core` is linked in-process on purpose: the UI calls it directly and the agent never
//! opens a local socket (r3 §7).

fn main() {
    println!(
        "rfm-agent {} — protocol v{}, alpn {}",
        env!("CARGO_PKG_VERSION"),
        rfm_core::PROTOCOL_VERSION,
        String::from_utf8_lossy(rfm_core::ALPN),
    );
}
