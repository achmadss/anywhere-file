//! Our relay fleet, as the agents see it.
//!
//! The agents get [`relay_mode`]: a custom map with our relays and nothing
//! else. iroh's `Default` and `Staging` modes both point at n0's public
//! relays, so nothing in this crate can produce them. [`assert_no_public_relay`]
//! is the check the CI test runs.

use std::collections::HashSet;

use iroh::{RelayMode, RelayUrl};

/// Env var carrying the fleet: comma separated relay URLs, e.g.
/// `RFM_RELAY_URLS=https://relay-eu-west.example.com,https://relay-ap-se.example.com`.
pub const RELAY_URLS_ENV: &str = "RFM_RELAY_URLS";

/// Host suffixes of relays our traffic must never touch. n0's production and
/// staging relays both live under `n0.iroh.link`; `iroh.network` is the older
/// public relay host. Our hostnames must not end here.
pub const PUBLIC_RELAY_SUFFIXES: &[&str] = &["n0.iroh.link", "iroh.network"];

/// Placeholder fleet used when [`RELAY_URLS_ENV`] is unset: the two deploy
/// regions with example hostnames. Real runs set the env var; the operator
/// values live in `relay/deploy/regions/*.env`, never here.
pub fn example_urls() -> Vec<RelayUrl> {
    [
        "https://relay-eu-west.example.com",
        "https://relay-ap-southeast.example.com",
    ]
    .into_iter()
    .map(|s| s.parse().expect("example fleet URL parses"))
    .collect()
}

/// The fleet from [`RELAY_URLS_ENV`], or `None` when it is unset.
pub fn relay_urls_from_env() -> Option<Vec<RelayUrl>> {
    let raw = std::env::var(RELAY_URLS_ENV).ok()?;
    let urls: Vec<RelayUrl> = raw
        .split(',')
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .map(|s| s.parse().expect("RFM_RELAY_URLS entry parses as a URL"))
        .collect();
    Some(urls)
}

/// True when the URL points at a public relay we must never use.
pub fn is_public_relay(url: &RelayUrl) -> bool {
    let host = url.host_str().unwrap_or("");
    PUBLIC_RELAY_SUFFIXES.iter().any(|suffix| {
        host == *suffix
            || host
                .strip_suffix(suffix)
                .is_some_and(|rest| rest.ends_with('.'))
    })
}

/// The relay mode agents get: a custom map with exactly these URLs.
///
/// Panics on an empty list, a non-HTTPS URL, or a public relay URL. There is
/// no path through this function to `RelayMode::Default` or `Staging`.
pub fn relay_mode(urls: &[RelayUrl]) -> RelayMode {
    assert!(!urls.is_empty(), "fleet needs at least one relay URL");
    for url in urls {
        assert_eq!(url.scheme(), "https", "relay URL must be https: {url}");
        assert!(
            !is_public_relay(url),
            "relay URL is a public relay, refusing: {url}"
        );
    }
    RelayMode::custom(urls.iter().cloned())
}

/// Checks that a relay mode contacts only our relays.
///
/// Panics when the mode is not `Custom`, when it is empty, when any URL is a
/// public relay, or when any URL overlaps iroh's bundled production or staging
/// maps. Call it on whatever mode the agent is about to bind.
pub fn assert_no_public_relay(mode: &RelayMode) {
    let RelayMode::Custom(map) = mode else {
        panic!("agent relay mode must be Custom (our relays), got {mode:?}");
    };
    let urls: Vec<RelayUrl> = map.urls();
    assert!(!urls.is_empty(), "custom relay map is empty");
    for url in &urls {
        assert!(
            !is_public_relay(url),
            "agent is configured for a public relay: {url}"
        );
    }
    let mut bundled = HashSet::new();
    for url in iroh::defaults::prod::default_relay_map().urls::<Vec<_>>() {
        bundled.insert(url.to_string());
    }
    for url in iroh::defaults::staging::default_relay_map().urls::<Vec<_>>() {
        bundled.insert(url.to_string());
    }
    for url in &urls {
        assert!(
            !bundled.contains(&url.to_string()),
            "agent relay overlaps iroh's bundled public relays: {url}"
        );
    }
}

/// Waits until the endpoint has reached a relay, then returns its address.
///
/// rfm-core binds first and joins the home relay in the background. Tests that
/// must send bytes through a relay wait here instead of guessing a sleep.
pub async fn wait_for_relay(
    node: &rfm_core::transport::Node,
    timeout: std::time::Duration,
) -> iroh::EndpointAddr {
    let start = std::time::Instant::now();
    loop {
        let addr = node.addr();
        if addr.addrs.iter().any(|a| a.is_relay()) {
            return addr;
        }
        assert!(
            start.elapsed() < timeout,
            "endpoint reached no relay within {timeout:?}"
        );
        tokio::time::sleep(std::time::Duration::from_millis(100)).await;
    }
}

/// Keeps only the relay addresses of an endpoint address.
///
/// Dialling this forces the first path through a relay. Direct addresses the
/// two endpoints discover afterwards may still upgrade the connection, which
/// is what the failover test does not want, so deploy tests run the two ends
/// where no direct path exists or read `conn.conn_type()` to confirm relayed.
pub fn relay_only(addr: &iroh::EndpointAddr) -> iroh::EndpointAddr {
    iroh::EndpointAddr::from_parts(addr.id, addr.addrs.iter().filter(|a| a.is_relay()).cloned())
}

pub mod bench {
    //! Throughput harness. Measures bytes that crossed a relay, for issue #30.
    //!
    //! Two rfm-core endpoints (the real agent stack) dial through the given
    //! relay map and echo fixed frames until the deadline. The reported number
    //! is received bytes over elapsed seconds. Connection counts are not an
    //! input and not an output: #30 sizes relays in bytes per second.

    use std::time::{Duration, Instant};

    use iroh::{RelayMap, RelayMode, SecretKey};

    /// One frame per echo round trip. Matches the transport echo limit.
    pub const FRAME_BYTES: usize = 64 * 1024;

    /// What one bench run measured.
    pub struct BenchResult {
        /// Bytes received back through the relay.
        pub bytes_received: u64,
        /// Wall clock seconds for the run.
        pub seconds: f64,
    }

    impl BenchResult {
        /// Sustained received bytes per second. The #30 denominator.
        pub fn bytes_per_second(&self) -> f64 {
            self.bytes_received as f64 / self.seconds
        }
    }

    /// Echoes frames through the relay until `seconds` elapse, over `streams`
    /// parallel streams. Both directions cross the relay, so the relay moves
    /// roughly twice the reported bytes.
    pub async fn bench_throughput(
        relay_map: RelayMap,
        seconds: u64,
        streams: usize,
    ) -> Result<BenchResult, Box<dyn std::error::Error + Send + Sync>> {
        assert!(seconds > 0, "bench needs a positive duration");
        assert!(streams > 0, "bench needs at least one stream");

        let server = rfm_core::transport::Node::bind(rfm_core::transport::NodeConfig {
            secret_key: SecretKey::generate(),
            relay_mode: RelayMode::Custom(relay_map.clone()),
        })
        .await?;
        let client = rfm_core::transport::Node::bind(rfm_core::transport::NodeConfig {
            secret_key: SecretKey::generate(),
            relay_mode: RelayMode::Custom(relay_map),
        })
        .await?;

        let server_addr = super::wait_for_relay(&server, Duration::from_secs(30)).await;
        super::wait_for_relay(&client, Duration::from_secs(30)).await;
        let target = super::relay_only(&server_addr);

        let deadline = Instant::now() + Duration::from_secs(seconds);
        let started = Instant::now();
        let mut tasks = Vec::new();
        for _ in 0..streams {
            let endpoint = client.endpoint().clone();
            let target = target.clone();
            tasks.push(tokio::spawn(async move {
                let mut received: u64 = 0;
                let conn = endpoint.connect(target, rfm_core::ALPN).await?;
                let out: Result<u64, Box<dyn std::error::Error + Send + Sync>> = async move {
                    let frame = vec![0xA5u8; super::bench::FRAME_BYTES];
                    while Instant::now() < deadline {
                        let (mut send, mut recv) = conn.open_bi().await?;
                        send.write_all(&frame).await?;
                        send.finish()?;
                        let echo = recv.read_to_end(FRAME_BYTES).await?;
                        received += echo.len() as u64;
                    }
                    Ok(received)
                }
                .await;
                out
            }));
        }

        let mut bytes_received: u64 = 0;
        for task in tasks {
            bytes_received += task.await??;
        }
        let seconds = started.elapsed().as_secs_f64();

        client.shutdown().await;
        server.shutdown().await;

        Ok(BenchResult {
            bytes_received,
            seconds,
        })
    }
}
