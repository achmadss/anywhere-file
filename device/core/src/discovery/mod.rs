//! DNS-SD discovery on `_rfm._tcp` (r3 §11.1, decision D3).
//!
//! Devices find each other by ID, never by name. Every agent advertises one
//! `_rfm._tcp` record per workspace it belongs to, with TXT `ws=<workspace id>`,
//! `dev=<device key>`, `v=<trust-list version>`, and browses for the same.
//! Matching is on `ws`. Records for a workspace this device is not a member of
//! are ignored.
//!
//! The responder is `mdns-sd`, our own on every platform, picked by spike #3
//! (`docs/spikes/mdns.md`). It runs alongside iroh. Iroh's own address lookup
//! stays disabled, since nothing it publishes carries the `ws`/`dev`/`v` fields
//! matched on here.
//!
//! A peer found here is dialled by endpoint id with its LAN address only
//! ([`dial_lan`]), so a LAN session never leaves the network.

use std::{
    collections::{HashMap, HashSet},
    net::SocketAddr,
    str::FromStr,
    sync::Mutex,
    time::{Duration, Instant},
};

use iroh::{Endpoint, EndpointAddr, EndpointId, TransportAddr, endpoint::Connection};
use mdns_sd::{ServiceDaemon, ServiceEvent, ServiceInfo};

use crate::trust::WorkspaceId;

/// The DNS-SD service type, with the `.local.` suffix `mdns-sd` wants.
pub const SERVICE_TYPE: &str = "_rfm._tcp.local.";

/// A DNS label holds at most 63 bytes. `mdns-sd` drops longer instance names
/// from its packets without an error (spike #3), so a longer name is refused
/// here instead of being registered into silence.
pub const MAX_INSTANCE_NAME_LEN: usize = 63;

/// One resolved advertisement from a LAN peer.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DiscoveredPeer {
    /// The workspace from the `ws=` TXT field.
    pub workspace: WorkspaceId,
    /// The device key from the `dev=` TXT field, which is the iroh endpoint id.
    pub device: EndpointId,
    /// The trust-list version from the `v=` TXT field.
    pub version: u64,
    /// Where the peer's iroh endpoint listens, from the mDNS address records
    /// and the advertised port.
    pub addrs: Vec<SocketAddr>,
}

impl DiscoveredPeer {
    /// The dial target: endpoint id with the LAN addresses only, and no relay
    /// URL. See [`dial_lan`] for why no relay path can appear from this.
    pub fn lan_addr(&self) -> EndpointAddr {
        EndpointAddr::from_parts(
            self.device,
            self.addrs.iter().map(|a| TransportAddr::Ip(*a)),
        )
    }
}

/// Why an advertisement or a browse failed.
#[derive(Debug, thiserror::Error)]
pub enum DiscoveryError {
    /// The responder or querier reported a failure.
    #[error("mDNS failed: {0}")]
    Mdns(#[from] mdns_sd::Error),
    /// The instance name would not fit in a DNS label and was not registered.
    #[error("instance name is {len} bytes, the limit is {MAX_INSTANCE_NAME_LEN}")]
    NameTooLong {
        /// The length that did not fit.
        len: usize,
    },
    /// A resolved record did not carry usable `ws`/`dev`/`v` fields.
    #[error("bad TXT record: {0}")]
    BadRecord(String),
}

/// Builds an instance name from two tags, refusing names over the DNS label
/// limit instead of registering something no querier will ever see.
fn instance_name(device_tag: &str, workspace_tag: &str) -> Result<String, DiscoveryError> {
    let name = format!("{device_tag}-{workspace_tag}");
    if name.len() > MAX_INSTANCE_NAME_LEN {
        return Err(DiscoveryError::NameTooLong { len: name.len() });
    }
    Ok(name)
}

/// Builds the instance name for one (device, workspace) pair.
///
/// Real device keys (64 hex chars) and workspace ids (32 hex chars) never fit
/// side by side, so the device half is its first 16 hex chars. The full key
/// travels in the `dev=` TXT field, where the limit is 255 bytes per value.
/// The name only has to be distinct per pair on one LAN.
pub fn instance_name_for(device: &EndpointId, workspace: &WorkspaceId) -> String {
    let dev = device.to_string();
    let short = dev.get(..16).unwrap_or(&dev);
    instance_name(short, &workspace.to_string())
        .expect("16 hex chars plus a workspace id always fit in 63 bytes")
}

/// The hostname all of one device's records share, so address records go out
/// once no matter how many workspaces the device is in.
fn host_name(device: &EndpointId) -> String {
    let dev = device.to_string();
    let short = dev.get(..16).unwrap_or(&dev);
    format!("rfm-{short}.local.")
}

fn txt_properties(
    workspace: &WorkspaceId,
    device: &EndpointId,
    version: u64,
) -> HashMap<String, String> {
    HashMap::from([
        ("ws".to_string(), workspace.to_string()),
        ("dev".to_string(), device.to_string()),
        ("v".to_string(), version.to_string()),
    ])
}

/// Parses one resolved service into a peer, or says why it is unusable.
/// Records for other workspaces are not an error here. The caller drops them
/// by comparing `workspace` against its membership.
fn parse_resolved(
    fullname: &str,
    txt: &dyn Fn(&str) -> Option<String>,
    port: u16,
    addresses: &HashSet<mdns_sd::ScopedIp>,
) -> Result<DiscoveredPeer, DiscoveryError> {
    let missing = |k: &str| DiscoveryError::BadRecord(format!("{fullname} has no `{k}` TXT field"));
    let ws = txt("ws").ok_or_else(|| missing("ws"))?;
    let dev = txt("dev").ok_or_else(|| missing("dev"))?;
    let v = txt("v").ok_or_else(|| missing("v"))?;
    let workspace = WorkspaceId::from_str(&ws)
        .map_err(|_| DiscoveryError::BadRecord(format!("{fullname} has bad `ws={ws}`")))?;
    let device = EndpointId::from_str(&dev)
        .map_err(|_| DiscoveryError::BadRecord(format!("{fullname} has bad `dev`")))?;
    let version: u64 = v
        .parse()
        .map_err(|_| DiscoveryError::BadRecord(format!("{fullname} has bad `v={v}`")))?;
    let mut addrs: Vec<SocketAddr> = addresses
        .iter()
        .map(|ip| SocketAddr::new(ip.to_ip_addr(), port))
        .collect();
    addrs.sort();
    Ok(DiscoveredPeer {
        workspace,
        device,
        version,
        addrs,
    })
}

/// Advertises `_rfm._tcp`, one record per workspace.
pub struct Advertiser {
    daemon: ServiceDaemon,
    device: EndpointId,
    port: u16,
    /// Instance full names by workspace, so `v=` can be re-registered later.
    registered: Mutex<HashMap<WorkspaceId, String>>,
}

impl Advertiser {
    /// Starts the responder and remembers which iroh port to publish.
    pub fn start(device: EndpointId, port: u16) -> Result<Self, DiscoveryError> {
        Ok(Self {
            daemon: ServiceDaemon::new()?,
            device,
            port,
            registered: Mutex::new(HashMap::new()),
        })
    }

    /// Starts the responder for the iroh endpoint's own bound port.
    ///
    /// The published port is the endpoint's IPv4 socket, which is what a LAN
    /// peer dials. A device in several workspaces calls [`Self::advertise`]
    /// once per workspace after this.
    pub fn for_endpoint(endpoint: &Endpoint) -> Result<Self, DiscoveryError> {
        let port = endpoint
            .bound_sockets()
            .into_iter()
            .find(|s| s.is_ipv4())
            .map(|s| s.port())
            .ok_or_else(|| DiscoveryError::BadRecord("endpoint has no IPv4 socket".into()))?;
        Self::start(endpoint.id(), port)
    }

    /// Advertises membership of `workspace` at trust-list `version`.
    pub fn advertise(
        &self,
        workspace: WorkspaceId,
        version: u64,
    ) -> Result<String, DiscoveryError> {
        let instance = instance_name_for(&self.device, &workspace);
        let info = ServiceInfo::new(
            SERVICE_TYPE,
            &instance,
            &host_name(&self.device),
            (),
            self.port,
            txt_properties(&workspace, &self.device, version),
        )?
        .enable_addr_auto();
        let fullname = info.get_fullname().to_string();
        self.daemon.register(info)?;
        self.registered
            .lock()
            .unwrap()
            .insert(workspace, fullname.clone());
        Ok(fullname)
    }

    /// Re-publishes the `v=` TXT field after the trust list changed.
    ///
    /// This is the hook issue #10 drives: when a device signs or accepts a new
    /// trust-list version, it calls this so peers with a lower version know to
    /// pull the list from it (r3 §8.4). Re-registering under the same instance
    /// name replaces the record rather than adding one.
    pub fn set_version(&self, workspace: WorkspaceId, version: u64) -> Result<(), DiscoveryError> {
        self.advertise(workspace, version)?;
        Ok(())
    }

    /// Stops the responder.
    pub fn shutdown(self) -> Result<(), DiscoveryError> {
        self.daemon.shutdown()?;
        Ok(())
    }
}

/// Browses `_rfm._tcp` and matches on `ws`.
pub struct Browser {
    daemon: ServiceDaemon,
}

impl Browser {
    /// Starts the querier.
    pub fn start() -> Result<Self, DiscoveryError> {
        Ok(Self {
            daemon: ServiceDaemon::new()?,
        })
    }

    /// Collects peers in `workspace` until `deadline`, ignoring records for
    /// workspaces this device is not a member of.
    ///
    /// Poll in a loop for continuous discovery. The first call pays for the
    /// mDNS query round (tens of ms, or just over a second if the first query
    /// is lost, per spike #3). Later calls also see the daemon cache.
    pub fn browse_workspace(
        &self,
        workspace: WorkspaceId,
        deadline: Duration,
    ) -> Result<Vec<DiscoveredPeer>, DiscoveryError> {
        self.browse_until(workspace, deadline, usize::MAX)
    }

    /// Like [`Self::browse_workspace`], but returns as soon as `expect` peers
    /// are known, so a test can time discovery instead of timing the deadline.
    pub fn browse_until(
        &self,
        workspace: WorkspaceId,
        deadline: Duration,
        expect: usize,
    ) -> Result<Vec<DiscoveredPeer>, DiscoveryError> {
        let rx = self.daemon.browse(SERVICE_TYPE)?;
        let start = Instant::now();
        let mut peers: HashMap<EndpointId, DiscoveredPeer> = HashMap::new();
        while start.elapsed() < deadline && peers.len() < expect {
            let left = deadline.checked_sub(start.elapsed()).unwrap_or_default();
            match rx.recv_timeout(left) {
                Ok(ServiceEvent::ServiceResolved(info)) => {
                    let get = |k: &str| info.get_property_val_str(k).map(str::to_string);
                    match parse_resolved(&info.fullname, &get, info.port, &info.addresses) {
                        Ok(peer) if peer.workspace == workspace => {
                            peers.insert(peer.device, peer);
                        }
                        Ok(_) => {}
                        Err(_) => {}
                    }
                }
                Ok(_) => {}
                Err(_) => break,
            }
        }
        let mut out: Vec<DiscoveredPeer> = peers.into_values().collect();
        out.sort_by_key(|p| p.device);
        Ok(out)
    }

    /// Stops the querier.
    pub fn shutdown(self) -> Result<(), DiscoveryError> {
        self.daemon.shutdown()?;
        Ok(())
    }
}

/// Dials a discovered peer by endpoint id with its LAN address only.
///
/// The dial target carries IP addresses and no relay URL, so iroh has no relay
/// path to try. What was checked: `Endpoint::connect` resolves only the
/// addresses in the given `EndpointAddr` plus whatever the configured address
/// lookup services return (`socket.rs:1315`, `remote_map.rs:311-323`). This
/// crate always clears iroh's lookup services at bind
/// (`transport::Node::bind`), so a lookup returns `NoServiceConfigured` and
/// adds nothing (`remote_state.rs:896-903`). Relay candidates only ever come
/// from `TransportAddr::Relay` entries in the dial target
/// (`remote_state.rs:1502-1515`), and there are none here. With
/// `RelayMode::Disabled` there is additionally no relay transport to use at
/// all. The acceptance test `lan_dial_opens_no_relay_path` holds this by
/// checking every open path of the resulting connection.
pub async fn dial_lan(
    endpoint: &Endpoint,
    peer: &DiscoveredPeer,
    alpn: &[u8],
) -> Result<Connection, iroh::endpoint::ConnectError> {
    let addr = peer.lan_addr();
    debug_assert!(addr.relay_urls().next().is_none());
    endpoint.connect(addr, alpn).await
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn names_at_63_bytes_register_and_at_64_are_refused() {
        // 62 + 1 + 0 = 63 fits, 63 + 1 + 0 = 64 does not.
        let ok = "d".repeat(62);
        assert_eq!(instance_name(&ok, "").unwrap().len(), 63);
        let over = "d".repeat(63);
        assert!(matches!(
            instance_name(&over, ""),
            Err(DiscoveryError::NameTooLong { len: 64 })
        ));
    }

    #[test]
    fn real_keys_always_fit_the_label() {
        let device =
            EndpointId::from_str(&iroh::SecretKey::generate().public().to_string()).unwrap();
        let name = instance_name_for(&device, &WorkspaceId::generate());
        assert!(name.len() <= MAX_INSTANCE_NAME_LEN, "{name}");
    }

    #[test]
    fn a_record_round_trips_through_parse() {
        let device: EndpointId = iroh::SecretKey::generate().public();
        let workspace = WorkspaceId::generate();
        let txt = txt_properties(&workspace, &device, 7);
        let get = |k: &str| txt.get(k).cloned();
        let mut addresses = HashSet::new();
        let ip: std::net::IpAddr = "192.168.2.10".parse().unwrap();
        addresses.insert(mdns_sd::ScopedIp::from(ip));
        let peer = parse_resolved("x._rfm._tcp.local.", &get, 4433, &addresses).unwrap();
        assert_eq!(peer.workspace, workspace);
        assert_eq!(peer.device, device);
        assert_eq!(peer.version, 7);
        assert_eq!(
            peer.addrs,
            [SocketAddr::new("192.168.2.10".parse().unwrap(), 4433)]
        );
    }

    #[test]
    fn records_without_ws_dev_or_v_are_refused() {
        let get = |_: &str| None;
        let err = parse_resolved("x._rfm._tcp.local.", &get, 4433, &HashSet::new()).unwrap_err();
        assert!(matches!(err, DiscoveryError::BadRecord(_)));
    }

    #[test]
    fn the_lan_dial_target_carries_no_relay_url() {
        let device: EndpointId = iroh::SecretKey::generate().public();
        let peer = DiscoveredPeer {
            workspace: WorkspaceId::generate(),
            device,
            version: 1,
            addrs: vec!["127.0.0.1:4400".parse().unwrap()],
        };
        let addr = peer.lan_addr();
        assert!(addr.relay_urls().next().is_none());
        assert_eq!(addr.ip_addrs().count(), 1);
    }
}
