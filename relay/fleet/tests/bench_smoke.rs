//! The capacity harness works end to end over a local relay.
//!
//! This runs in CI. It proves `bench_throughput` moves real bytes through a
//! real relay process. The relay here is plain HTTP on loopback: the numbers
//! mean nothing, and production relays terminate TLS. The deployed numbers
//! come from relay/deploy/capacity/bench.sh.

use std::{net::Ipv4Addr, sync::Arc};

use iroh::{RelayMap, RelayUrl};
use iroh_relay::server::{AllowAll, RelayConfig as RelayServerConfig, Server, ServerConfig};

/// A relay on loopback with no TLS, for tests only.
async fn local_relay() -> (RelayMap, Server) {
    let mut relay = RelayServerConfig::new((Ipv4Addr::LOCALHOST, 0));
    relay.access = Arc::new(AllowAll);
    let mut config = ServerConfig::default();
    config.relay = Some(relay);
    let server = Server::spawn(config).await.expect("start local relay");
    let url: RelayUrl = format!("http://{}", server.http_addr().expect("http addr"))
        .parse()
        .expect("relay url parses");
    (RelayMap::from_iter([url]), server)
}

#[tokio::test]
async fn bench_moves_bytes_through_a_local_relay() {
    let (relay_map, _relay) = local_relay().await;

    let result = rfm_fleet::bench::bench_throughput(relay_map, 2, 1)
        .await
        .expect("bench run");

    assert!(
        result.bytes_received > 0,
        "bench moved no bytes through the local relay"
    );
    assert!(result.bytes_per_second() > 0.0);
}
