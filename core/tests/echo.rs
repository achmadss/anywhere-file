//! Two `rfm-core` instances on one machine, over ALPN `rfm/1`. Issue #5's acceptance.
//!
//! No relay and no address lookup, so the only way these two find each other is the
//! address one hands the other. That is deliberate: it is the same footing a LAN pair is
//! on, and it means a regression that re-enables discovery cannot be masked by discovery
//! quietly doing the work.

use std::time::Duration;

use iroh::{EndpointAddr, RelayMode, SecretKey, TransportAddr};
use rfm_core::{
    ALPN,
    config::ConfigDir,
    transport::{Node, NodeConfig},
};

async fn node() -> Node {
    Node::bind(NodeConfig {
        secret_key: SecretKey::generate(),
        relay_mode: RelayMode::Disabled,
    })
    .await
    .expect("bind")
}

/// The loopback address the endpoint is actually listening on. With no relay and no
/// lookup service this is the whole of what a peer needs.
fn loopback_addr(node: &Node) -> EndpointAddr {
    let port = node
        .endpoint()
        .bound_sockets()
        .into_iter()
        .find(|s| s.is_ipv4())
        .expect("an IPv4 socket")
        .port();
    EndpointAddr::from_parts(
        node.id(),
        [TransportAddr::Ip(([127, 0, 0, 1], port).into())],
    )
}

#[tokio::test]
async fn two_instances_echo_over_rfm_1() {
    let server_dir = tempfile::tempdir().unwrap();
    let client_dir = tempfile::tempdir().unwrap();
    ConfigDir::open(server_dir.path()).unwrap();
    ConfigDir::open(client_dir.path()).unwrap();

    let server = node().await;
    let client = node().await;

    let conn = client
        .endpoint()
        .connect(loopback_addr(&server), ALPN)
        .await
        .expect("connect");

    // The peer key comes from the handshake, never from anything sent inside the stream.
    // ADR 0002 item 3; #14's access rule reduces to nothing without it.
    assert_eq!(conn.remote_id(), server.id());

    let (mut send, mut recv) = conn.open_bi().await.unwrap();
    send.write_all(b"hello over rfm/1").await.unwrap();
    send.finish().unwrap();
    assert_eq!(
        recv.read_to_end(64 * 1024).await.unwrap(),
        b"hello over rfm/1"
    );

    conn.close(0u32.into(), b"done");
    client.shutdown().await;
    server.shutdown().await;
}

#[tokio::test]
async fn a_mismatched_alpn_is_refused() {
    let server = node().await;
    let client = node().await;

    let result = client
        .endpoint()
        .connect(loopback_addr(&server), b"rfm/0")
        .await;
    assert!(result.is_err(), "an endpoint on rfm/1 accepted rfm/0");

    client.shutdown().await;
    server.shutdown().await;
}

/// r3 §11.1: iroh's own address lookup is disabled and the cloud directory (#26) replaces
/// it. If this fails, device keys are being published to a third-party DNS or DHT.
#[tokio::test]
async fn no_address_lookup_service_is_started() {
    let node = node().await;
    let services = node.endpoint().address_lookup().expect("endpoint is open");
    assert!(
        services.is_empty(),
        "{} address lookup service(s) configured; r3 §11.1 allows none",
        services.len()
    );
    node.shutdown().await;
}

/// Shutdown is what has to work when the agent quits, so it is timed rather than merely
/// awaited: a close that hangs looks identical to one that works in a test that only
/// awaits it.
#[tokio::test]
async fn shutdown_stops_the_accept_task() {
    let node = node().await;
    tokio::time::timeout(Duration::from_secs(5), node.shutdown())
        .await
        .expect("shutdown hung");
}
