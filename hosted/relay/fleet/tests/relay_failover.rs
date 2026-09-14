//! A relay dies mid-transfer; the transfer still completes. Needs a deployed
//! fleet with at least two relays.
//!
//!   RFM_RELAY_URLS=https://relay-eu-west.example.com,https://relay-ap-se.example.com \
//!     cargo test --manifest-path relay/fleet/Cargo.toml --test relay_failover -- --ignored --nocapture
//!
//! Gated with `#[ignore]` plus the env var, so CI never runs it. Procedure:
//! start the test, wait for the `progress` lines, then kill one relay (stop
//! the container on that VPS). The client reconnects over the surviving relay
//! and resumes from the last acknowledged chunk. The test passes with or
//! without the kill; without it, it proves the resume machinery moves the
//! same bytes. With it, it proves the recovery.
//!
//! Chunks are acknowledged per frame, so resume is at frame granularity.

use std::time::{Duration, Instant};

use iroh::SecretKey;
use rfm_core::transport::{Node, NodeConfig};

const TRANSFER_BYTES: usize = 16 * 1024 * 1024;
const FRAME_BYTES: usize = 64 * 1024;
const TEST_TIMEOUT: Duration = Duration::from_secs(600);

fn fleet_mode() -> iroh::RelayMode {
    let urls = rfm_fleet::relay_urls_from_env().unwrap_or_else(|| {
        panic!(
            "set {} to at least two deployed relays",
            rfm_fleet::RELAY_URLS_ENV
        )
    });
    assert!(
        urls.len() >= 2,
        "failover needs at least two relays, got {}",
        urls.len()
    );
    let mode = rfm_fleet::relay_mode(&urls);
    rfm_fleet::assert_no_public_relay(&mode);
    mode
}

async fn connect(
    client: &Node,
    target: &iroh::EndpointAddr,
) -> Result<iroh::endpoint::Connection, Box<dyn std::error::Error + Send + Sync>> {
    Ok(tokio::time::timeout(
        Duration::from_secs(60),
        client.endpoint().connect(target.clone(), rfm_core::ALPN),
    )
    .await??)
}

#[tokio::test]
#[ignore = "needs a deployed relay fleet; see the file header"]
async fn killing_a_relay_mid_transfer_recovers() {
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

    let started = Instant::now();
    let frame: Vec<u8> = (0..FRAME_BYTES).map(|i| (i % 251) as u8).collect();
    let mut acked: usize = 0;
    let mut reconnects = 0;
    while acked < TRANSFER_BYTES {
        assert!(
            started.elapsed() < TEST_TIMEOUT,
            "transfer stalled at {acked}/{TRANSFER_BYTES} bytes"
        );
        let conn = match connect(&client, &target).await {
            Ok(conn) => conn,
            Err(err) => {
                reconnects += 1;
                eprintln!("reconnecting ({reconnects} so far): {err:#}");
                tokio::time::sleep(Duration::from_secs(2)).await;
                continue;
            }
        };
        loop {
            if acked >= TRANSFER_BYTES {
                break;
            }
            let (mut send, mut recv) = match conn.open_bi().await {
                Ok(pair) => pair,
                Err(_) => break, // connection died; reconnect above
            };
            let offset = acked;
            let expected = frame.clone();
            let write = async {
                send.write_all(&expected).await?;
                send.finish()?;
                Ok::<_, Box<dyn std::error::Error + Send + Sync>>(())
            }
            .await;
            if write.is_err() {
                break;
            }
            match recv.read_to_end(FRAME_BYTES).await {
                Ok(echo) if echo == expected => {
                    acked = offset + echo.len();
                    if acked % (4 * 1024 * 1024) < FRAME_BYTES {
                        eprintln!(
                            "progress: {acked}/{TRANSFER_BYTES} bytes, {reconnects} reconnects"
                        );
                    }
                }
                _ => break, // short read or mismatch; reconnect and resend
            }
        }
        if acked < TRANSFER_BYTES {
            reconnects += 1;
            eprintln!("reconnecting ({reconnects} so far)");
            tokio::time::sleep(Duration::from_secs(2)).await;
        }
    }

    eprintln!(
        "done: {TRANSFER_BYTES} bytes in {:.1}s with {reconnects} reconnects",
        started.elapsed().as_secs_f64()
    );
    client.shutdown().await;
    server.shutdown().await;
}
