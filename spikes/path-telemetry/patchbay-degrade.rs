//! Spike #42, the degradation direction: a direct path that stops working.
//!
//! Upstream's patchbay suite covers relay to direct and direct to a better direct. It has
//! nothing that kills a working direct path while leaving the relay reachable, which is the
//! case #14 cares about, because that is when a connection becomes subject to the r3 §9
//! step 4 relay rule again.
//!
//! Not part of this repo's build. patchbay needs Linux user namespaces, so run it in a
//! container against the pinned iroh source:
//!
//! ```sh
//! relay/apply.sh                                    # if relay/src is missing
//! cp spikes/path-telemetry/patchbay-degrade.rs "$SRC/iroh/tests/patchbay/degrade42.rs"
//! # add `#[path = "patchbay/degrade42.rs"] mod degrade42;` to iroh/tests/patchbay.rs
//! docker run --rm --privileged -v "$SRC:/src" -w /src rust:1.98-bookworm bash -c \
//!   'apt-get update -qq && apt-get install -y -qq nftables iproute2 >/dev/null &&
//!    cargo test --release -p iroh --test patchbay degrade42 -- --nocapture'
//! ```
//!
//! Do this against a copy of `relay/src`, never the checkout itself.

use std::time::{Duration, Instant};

use iroh::endpoint::{PathEvent, Side};
use n0_error::{Result, StackResultExt};
use n0_future::StreamExt;
use n0_tracing_test::traced_test;
use patchbay::Nat;
use testdir::testdir;
use tracing::info;

use crate::util::{Pair, PathConnectionExt, lab_with_relay, ping_accept, ping_open};

/// Holepunches to direct, then replugs one side behind a symmetric NAT so the direct path
/// dies while the relay stays reachable. Asserts that the selected path returns to the
/// relay, and reports how long that took from the replug.
async fn run_direct_to_relay(replug_side: Side) -> Result {
    let (lab, relay_map, _relay_guard, guard) = lab_with_relay(testdir!()).await?;
    let nat_easy = lab
        .add_router("nat_easy")
        .nat(Nat::Moderate)
        .build()
        .await?;
    let nat_hard = lab.add_router("nat_hard").nat(Nat::Strict).build().await?;
    let nat_peer = lab
        .add_router("nat_peer")
        .nat(Nat::Moderate)
        .build()
        .await?;

    let replug = lab
        .add_device("replug")
        .uplink(nat_easy.id())
        .build()
        .await?;
    let stable = lab
        .add_device("stable")
        .uplink(nat_peer.id())
        .build()
        .await?;

    let timeout = Duration::from_secs(15);
    let watch = Duration::from_secs(40);

    Pair::new(relay_map)
        .left(replug_side, replug, async move |dev, _ep, conn| {
            conn.wait_ip(timeout).await.context("initial holepunch")?;
            info!("holepunched to direct");

            // Subscribe before the change, so nothing between here and the first poll of
            // the stream can be missed.
            let mut events = conn.path_events();

            info!("replug behind a symmetric NAT");
            dev.iface("eth0").unwrap().replug(nat_hard.id()).await?;
            let replugged = Instant::now();

            let pinger = {
                let conn = conn.clone();
                tokio::spawn(async move {
                    let deadline = Instant::now() + watch;
                    while Instant::now() < deadline {
                        let _ = ping_accept(&conn, Duration::from_secs(10)).await;
                    }
                })
            };

            let mut fell_back = None;
            let observe = tokio::time::timeout(watch, async {
                while let Some(event) = events.next().await {
                    let at = replugged.elapsed();
                    match event {
                        PathEvent::Selected { remote_addr, .. } => {
                            info!("[{at:?}] Selected {remote_addr}");
                            if remote_addr.is_relay() {
                                return Some(at);
                            }
                        }
                        PathEvent::Closed { remote_addr, .. } => {
                            info!("[{at:?}] Closed {remote_addr}")
                        }
                        PathEvent::Opened { remote_addr, .. } => {
                            info!("[{at:?}] Opened {remote_addr}")
                        }
                        _ => {}
                    }
                }
                None
            })
            .await;
            if let Ok(Some(at)) = observe {
                fell_back = Some(at);
            }
            pinger.abort();

            match fell_back {
                Some(at) => info!("FELL BACK TO RELAY after {at:?}"),
                None => info!("NO RELAY FALLBACK OBSERVED within {watch:?}"),
            }
            conn.close(0u32.into(), b"bye");
            Ok(())
        })
        .right(stable, async move |_dev, _ep, conn| {
            conn.wait_ip(timeout).await.context("initial holepunch")?;
            let deadline = Instant::now() + watch;
            while Instant::now() < deadline {
                let _ = ping_open(&conn, Duration::from_secs(10)).await;
                if conn.close_reason().is_some() {
                    break;
                }
            }
            conn.closed().await;
            Ok(())
        })
        .run()
        .await?;
    guard.ok();
    Ok(())
}

#[tokio::test]
#[traced_test]
async fn degrade42_direct_to_relay_client() -> Result {
    run_direct_to_relay(Side::Client).await
}
