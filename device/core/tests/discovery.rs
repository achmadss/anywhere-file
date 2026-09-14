//! DNS-SD discovery acceptance for issue #9 (r3 §11.1).
//!
//! These tests run against real multicast DNS on loopback. They need no
//! Internet, gateway, or DNS server, only an interface with multicast
//! loopback, which the spike (`docs/spikes/mdns.md`) showed is enough. Each
//! test mints fresh workspace ids so parallel tests never see each other.

use std::time::{Duration, Instant};

use iroh::{RelayMode, SecretKey};
use rfm_core::{
    ALPN,
    discovery::{Advertiser, Browser, dial_lan},
    transport::{Node, NodeConfig},
    trust::WorkspaceId,
};

async fn node() -> Node {
    Node::bind(NodeConfig {
        secret_key: SecretKey::generate(),
        relay_mode: RelayMode::Disabled,
    })
    .await
    .expect("bind")
}

/// Advertises `workspace` at `version` from the node's own bound port.
fn advertise(node: &Node, workspace: WorkspaceId, version: u64) -> Advertiser {
    let adv = Advertiser::for_endpoint(node.endpoint()).expect("responder starts");
    adv.advertise(workspace, version).expect("register");
    adv
}

/// Two agents in the same workspace find each other in under 2 seconds.
#[tokio::test]
async fn same_workspace_peers_find_each_other_under_2_seconds() {
    let ws = WorkspaceId::generate();
    let a = node().await;
    let b = node().await;
    let _adv_a = advertise(&a, ws, 1);
    let _adv_b = advertise(&b, ws, 1);

    let browser = Browser::start().expect("querier starts");
    let start = Instant::now();
    let peers = browser
        .browse_until(ws, Duration::from_secs(5), 2)
        .expect("browse");
    let elapsed = start.elapsed();

    let ids: Vec<_> = peers.iter().map(|p| p.device).collect();
    assert!(ids.contains(&a.id()), "missing peer A in {ids:?}");
    assert!(ids.contains(&b.id()), "missing peer B in {ids:?}");
    assert!(
        elapsed < Duration::from_secs(2),
        "discovery took {elapsed:?}, the target is under 2 s"
    );

    browser.shutdown().expect("querier stops");
    a.shutdown().await;
    b.shutdown().await;
}

/// Two agents in different workspaces on one LAN do not connect and do not
/// appear in each other's device lists.
#[tokio::test]
async fn different_workspaces_stay_invisible_to_each_other() {
    let ws_a = WorkspaceId::generate();
    let ws_b = WorkspaceId::generate();
    let a = node().await;
    let b = node().await;
    let _adv_a = advertise(&a, ws_a, 1);
    let _adv_b = advertise(&b, ws_b, 1);

    let browser = Browser::start().expect("querier starts");
    let in_a = browser
        .browse_until(ws_a, Duration::from_secs(5), 1)
        .expect("browse A");
    let in_b = browser
        .browse_until(ws_b, Duration::from_secs(5), 1)
        .expect("browse B");

    let ids_a: Vec<_> = in_a.iter().map(|p| p.device).collect();
    let ids_b: Vec<_> = in_b.iter().map(|p| p.device).collect();
    assert_eq!(ids_a, [a.id()], "workspace A sees {ids_a:?}");
    assert_eq!(ids_b, [b.id()], "workspace B sees {ids_b:?}");

    browser.shutdown().expect("querier stops");
    a.shutdown().await;
    b.shutdown().await;
}

/// A device in two workspaces is discovered by peers in both.
#[tokio::test]
async fn a_device_in_two_workspaces_is_found_by_both() {
    let ws_a = WorkspaceId::generate();
    let ws_b = WorkspaceId::generate();
    let multi = node().await;
    let peer_a = node().await;
    let peer_b = node().await;

    let adv = Advertiser::for_endpoint(multi.endpoint()).expect("responder starts");
    adv.advertise(ws_a, 3).expect("register A");
    adv.advertise(ws_b, 5).expect("register B");
    let _adv_a = advertise(&peer_a, ws_a, 3);
    let _adv_b = advertise(&peer_b, ws_b, 5);

    let browser = Browser::start().expect("querier starts");
    let in_a = browser
        .browse_until(ws_a, Duration::from_secs(5), 2)
        .expect("browse A");
    let in_b = browser
        .browse_until(ws_b, Duration::from_secs(5), 2)
        .expect("browse B");

    assert!(
        in_a.iter().any(|p| p.device == multi.id()),
        "ws A: {in_a:?}"
    );
    assert!(
        in_b.iter().any(|p| p.device == multi.id()),
        "ws B: {in_b:?}"
    );
    assert_eq!(
        in_a.iter()
            .find(|p| p.device == multi.id())
            .unwrap()
            .version,
        3
    );
    assert_eq!(
        in_b.iter()
            .find(|p| p.device == multi.id())
            .unwrap()
            .version,
        5
    );

    browser.shutdown().expect("querier stops");
    multi.shutdown().await;
    peer_a.shutdown().await;
    peer_b.shutdown().await;
}

/// A peer found here is dialled by endpoint id with the LAN address only, and
/// the session carries real traffic without any relay path.
#[tokio::test]
async fn lan_dial_opens_no_relay_path() {
    let ws = WorkspaceId::generate();
    let server = node().await;
    let client = node().await;
    let _adv = advertise(&server, ws, 1);

    let browser = Browser::start().expect("querier starts");
    let peers = browser
        .browse_until(ws, Duration::from_secs(5), 1)
        .expect("browse");
    let peer = peers
        .iter()
        .find(|p| p.device == server.id())
        .expect("server discovered");

    // The dial target carries LAN addresses and no relay URL.
    let target = peer.lan_addr();
    assert!(target.relay_urls().next().is_none());
    assert!(target.ip_addrs().next().is_some());

    let conn = dial_lan(client.endpoint(), peer, ALPN)
        .await
        .expect("LAN dial");
    assert_eq!(conn.remote_id(), server.id());

    // iroh added no relay path to the dial. With no relay URL in the target
    // and no lookup service on either endpoint, there is nothing a relay path
    // could have been built from.
    let paths = conn.paths();
    assert!(!paths.is_empty(), "no open paths on the connection");
    assert!(
        paths.iter().all(|p| !p.is_relay()),
        "a relay path appeared on a LAN-only dial"
    );

    // The session works: the transport still echoes until #15 replaces it.
    let (mut send, mut recv) = conn.open_bi().await.expect("open stream");
    send.write_all(b"over lan").await.expect("write");
    send.finish().expect("finish");
    assert_eq!(
        recv.read_to_end(64 * 1024).await.expect("read"),
        b"over lan"
    );

    conn.close(0u32.into(), b"done");
    browser.shutdown().expect("querier stops");
    client.shutdown().await;
    server.shutdown().await;
}

/// `set_version` re-publishes `v=`, which is the hook issue #10 drives to pull
/// the trust list from whoever is higher (r3 §8.4).
#[tokio::test]
async fn a_version_bump_is_visible_to_peers() {
    let ws = WorkspaceId::generate();
    let a = node().await;
    let adv = Advertiser::for_endpoint(a.endpoint()).expect("responder starts");
    adv.advertise(ws, 1).expect("register");

    let browser = Browser::start().expect("querier starts");
    let seen_v1 = browser
        .browse_until(ws, Duration::from_secs(5), 1)
        .expect("browse");
    assert_eq!(seen_v1[0].version, 1);

    adv.set_version(ws, 2).expect("bump");

    let deadline = Instant::now() + Duration::from_secs(8);
    let bumped = loop {
        let peers = browser
            .browse_workspace(ws, Duration::from_secs(2))
            .expect("browse");
        if let Some(peer) = peers.iter().find(|p| p.device == a.id())
            && peer.version == 2
        {
            break true;
        }
        if Instant::now() >= deadline {
            break false;
        }
    };
    assert!(bumped, "peers never saw v=2");

    browser.shutdown().expect("querier stops");
    a.shutdown().await;
}
