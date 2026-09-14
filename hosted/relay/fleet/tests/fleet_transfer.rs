//! Two agents transfer through our relay. Needs a deployed fleet.
//!
//!   RFM_RELAY_URLS=https://relay-eu-west.example.com,https://relay-ap-se.example.com \
//!     cargo test --manifest-path relay/fleet/Cargo.toml --test fleet_transfer -- --ignored --nocapture
//!
//! Gated with `#[ignore]` plus the env var, so CI never runs it. The transfer
//! dials a relay-only address, so bytes have no direct path to take.

use std::time::Duration;

use iroh::SecretKey;
use rfm_core::transport::{Node, NodeConfig};

/// Total bytes moved in the transfer. Small enough to finish fast, large
/// enough to span many frames.
const TRANSFER_BYTES: usize = 8 * 1024 * 1024;
const FRAME_BYTES: usize = 64 * 1024;

fn fleet_mode() -> iroh::RelayMode {
    let urls = rfm_fleet::relay_urls_from_env().unwrap_or_else(|| {
        panic!(
            "set {} to the deployed fleet, e.g. {}=https://relay-eu-west.example.com",
            rfm_fleet::RELAY_URLS_ENV,
            rfm_fleet::RELAY_URLS_ENV
        )
    });
    let mode = rfm_fleet::relay_mode(&urls);
    rfm_fleet::assert_no_public_relay(&mode);
    mode
}

#[tokio::test]
#[ignore = "needs a deployed relay fleet; see the file header"]
async fn two_agents_transfer_through_our_relay() {
    let mode = fleet_mode();

    let server = Node::bind(NodeConfig {
        secret_key: SecretKey::generate(),
        relay_mode: mode.clone(),
    })
    .await
    .expect("bind server");
    let client = Node::bind(NodeConfig {
        secret_key: SecretKey::generate(),
        relay_mode: mode,
    })
    .await
    .expect("bind client");

    let server_addr = rfm_fleet::wait_for_relay(&server, Duration::from_secs(60)).await;
    rfm_fleet::wait_for_relay(&client, Duration::from_secs(60)).await;
    let target = rfm_fleet::relay_only(&server_addr);

    let conn = tokio::time::timeout(
        Duration::from_secs(60),
        client.endpoint().connect(target, rfm_core::ALPN),
    )
    .await
    .expect("connect timed out")
    .expect("connect");

    let mut sent = 0usize;
    let mut received = 0usize;
    let frame = vec![0x3Cu8; FRAME_BYTES];
    while sent < TRANSFER_BYTES {
        let (mut send, mut recv) = conn.open_bi().await.expect("open stream");
        send.write_all(&frame).await.expect("write frame");
        send.finish().expect("finish");
        let echo = recv.read_to_end(FRAME_BYTES).await.expect("read echo");
        assert_eq!(echo, frame, "relay altered a frame");
        sent += frame.len();
        received += echo.len();
    }
    assert_eq!(sent, TRANSFER_BYTES);
    assert_eq!(received, TRANSFER_BYTES);

    conn.close(0u32.into(), b"done");
    client.shutdown().await;
    server.shutdown().await;
}
