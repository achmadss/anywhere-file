//! Spike #42: observe iroh's relayed -> direct path transition on one machine.
//!
//! Runs a local iroh-relay, binds two endpoints, dials with the relay address only so the
//! connection has to start relayed, then watches `Connection::path_events()` while a single
//! bidirectional stream is transferring. Everything runs over loopback, so the timings say
//! how fast the telemetry reports a transition, not how fast a real hole punch completes.
//!
//! Throwaway. Not a workspace member. `cargo run --release` from this directory.

use std::{
    sync::{
        Arc,
        atomic::{AtomicU64, Ordering},
    },
    time::{Duration, Instant},
};

use iroh::{
    Endpoint, EndpointAddr, RelayMode,
    endpoint::{Connection, PathEvent, presets},
    test_utils::run_relay_server,
    tls::CaTlsConfig,
};
use tokio_stream::StreamExt;

type Res<T = ()> = Result<T, Box<dyn std::error::Error + Send + Sync>>;

const ALPN: &[u8] = b"spike/path-telemetry/1";

/// Payload size for the single stream that spans the path transition.
const PAYLOAD: usize = 256 * 1024 * 1024;
const CHUNK: usize = 64 * 1024;

/// How often the polling comparison task samples `Connection::paths()`.
const POLL_INTERVAL: Duration = Duration::from_secs(1);

#[tokio::main]
async fn main() -> Res {
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .init();

    let (relay_map, relay_url, _relay) = run_relay_server().await?;
    println!("local relay: {relay_url}");

    let server = Endpoint::builder(presets::Minimal)
        .relay_mode(RelayMode::Custom(relay_map.clone()))
        .ca_tls_config(CaTlsConfig::insecure_skip_verify())
        .alpns(vec![ALPN.to_vec()])
        .bind()
        .await?;
    let client = Endpoint::builder(presets::Minimal)
        .relay_mode(RelayMode::Custom(relay_map))
        .ca_tls_config(CaTlsConfig::insecure_skip_verify())
        .bind()
        .await?;

    server.online().await;
    client.online().await;

    // Drop every direct address from the dial target. The client knows only the relay, so
    // the first selected path is a relay path; the direct one can only appear afterwards,
    // from addresses exchanged over the relay.
    let full = server.addr();
    let relay_only = EndpointAddr::from_parts(full.id, full.addrs.into_iter().filter(|a| a.is_relay()));
    println!("dialling with relay-only address: {relay_only:?}");

    // SPIKE_IDLE=1 skips the transfer: does a connection with no application traffic still
    // get upgraded, and does the telemetry still report it?
    let idle = std::env::var("SPIKE_IDLE").is_ok();

    let server_handle = server.clone();
    let accept = tokio::spawn(async move {
        let conn = server.accept().await.expect("closed").await?;
        if idle {
            conn.closed().await;
            return Ok(0);
        }
        let (mut send, mut recv) = conn.accept_bi().await?;
        let mut total = 0u64;
        let mut buf = vec![0u8; CHUNK];
        while let Some(n) = recv.read(&mut buf).await? {
            total += n as u64;
        }
        send.write_all(&total.to_be_bytes()).await?;
        send.finish()?;
        conn.closed().await;
        Ok::<_, Box<dyn std::error::Error + Send + Sync>>(total)
    });

    let t0 = Instant::now();
    let conn = client.connect(relay_only, ALPN).await?;
    let connected_at = t0.elapsed();
    let sent = Arc::new(AtomicU64::new(0));

    // Subscribe before touching anything else: the stream is a broadcast channel, so a
    // transition between the subscribe and the first read cannot be missed.
    let events = conn.path_events();

    println!("\n[{:>9.3?}] connect() returned", connected_at);
    print_paths(&conn, t0, "after connect");

    let watcher = tokio::spawn(watch_events(events, t0, sent.clone()));
    let poller = tokio::spawn(poll_paths(conn.clone(), t0));

    if idle {
        println!("[{:>9.3?}] idle mode: no streams, watching for 10s", t0.elapsed());
        tokio::time::sleep(Duration::from_secs(10)).await;
        print_paths(&conn, t0, "after 10s idle");
        conn.close(0u32.into(), b"done");
        let _ = accept.await;
        let _ = watcher.await;
        poller.abort();
        client.close().await;
        server_handle.close().await;
        return Ok(());
    }

    // One stream, opened while relayed, spanning the transition.
    let (mut send, mut recv) = conn.open_bi().await?;
    let chunk = vec![0xABu8; CHUNK];
    let started = t0.elapsed();
    println!("[{started:>9.3?}] opened bi stream, writing {PAYLOAD} bytes");
    let mut written = 0usize;
    while written < PAYLOAD {
        let n = (PAYLOAD - written).min(CHUNK);
        send.write_all(&chunk[..n]).await?;
        written += n;
        sent.store(written as u64, Ordering::Relaxed);
    }
    send.finish()?;
    let mut ack = [0u8; 8];
    recv.read_exact(&mut ack).await?;
    let acked = u64::from_be_bytes(ack);
    let done = t0.elapsed();
    println!("[{done:>9.3?}] stream finished, peer acknowledged {acked} bytes");
    assert_eq!(acked as usize, PAYLOAD, "single stream lost bytes across the transition");

    print_paths(&conn, t0, "at end of transfer");
    conn.close(0u32.into(), b"done");
    let received = accept.await??;
    assert_eq!(received as usize, PAYLOAD);
    let _ = watcher.await;
    poller.abort();
    client.close().await;
    server_handle.close().await;
    Ok(())
}

/// Prints one snapshot from `Connection::paths()`.
fn print_paths(conn: &Connection, t0: Instant, label: &str) {
    let at = t0.elapsed();
    for p in conn.paths().iter() {
        let kind = if p.is_relay() { "relay " } else { "direct" };
        println!(
            "[{at:>9.3?}]   paths(): {kind} id={} selected={} rtt={:?} addr={}",
            p.id(),
            p.is_selected(),
            p.rtt(),
            p.remote_addr()
        );
    }
    println!("[{at:>9.3?}]   ^ snapshot: {label}");
}

/// Consumes the push stream and prints every event with its offset from `t0`.
async fn watch_events(
    mut events: iroh::endpoint::PathEventStream,
    t0: Instant,
    sent: Arc<AtomicU64>,
) {
    while let Some(event) = events.next().await {
        let at = t0.elapsed();
        let bytes = sent.load(Ordering::Relaxed);
        match event {
            PathEvent::Opened {
                id, remote_addr, ..
            } => println!("[{at:>9.3?}] EVENT Opened   id={id} {remote_addr} (bytes written so far: {bytes})"),
            PathEvent::Selected {
                id, remote_addr, ..
            } => println!("[{at:>9.3?}] EVENT Selected id={id} {remote_addr} (bytes written so far: {bytes})"),
            PathEvent::Closed {
                id,
                remote_addr,
                last_stats,
                ..
            } => println!(
                "[{at:>9.3?}] EVENT Closed   id={id} {remote_addr} tx={} rx={}",
                last_stats.udp_tx.bytes, last_stats.udp_rx.bytes
            ),
            PathEvent::Lagged { missed, .. } => {
                println!("[{at:>9.3?}] EVENT Lagged   missed={missed}")
            }
            other => println!("[{at:>9.3?}] EVENT {other:?}"),
        }
    }
    println!("[{:>9.3?}] event stream ended", t0.elapsed());
}

/// The alternative to the push stream: sample `paths()` on a timer and report the first
/// sample that shows a direct path selected.
async fn poll_paths(conn: Connection, t0: Instant) {
    loop {
        tokio::time::sleep(POLL_INTERVAL).await;
        let direct = conn
            .paths()
            .iter()
            .any(|p| p.is_selected() && p.is_ip());
        if direct {
            println!(
                "[{:>9.3?}] POLL   first {POLL_INTERVAL:?} sample that sees a direct path selected",
                t0.elapsed()
            );
            return;
        }
        if conn.close_reason().is_some() {
            return;
        }
    }
}
