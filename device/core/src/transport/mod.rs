//! The transport.
//!
//! [ADR 0002](../../../docs/adr/0002-transport-seam.md) limits what the rest of the crate
//! may know about iroh to five capabilities, and this module is where all five live. The
//! abstract bidirectional stream everything above is written against arrives with #15,
//! which is the first caller that needs it; it lands in this directory so nothing moves.
//!
//! What is deliberately not here is configuration. Relay maps, timeouts and congestion
//! control are the agent's to choose at startup (#33) and arrive through [`NodeConfig`].
//! One exception, and it is a security property rather than a tuning knob: iroh's own
//! address lookup is off, unconditionally, because r3 §11.1 says the cloud directory (#26)
//! replaces it so that no third-party DNS or DHT sees a device key.

use iroh::{
    Endpoint, EndpointAddr, EndpointId, RelayMode, SecretKey,
    endpoint::{BindError, Connection, presets},
};
use tokio::task::JoinHandle;
use tracing::{debug, info, warn};

use crate::ALPN;

/// Ceiling on one echoed frame, until #15 defines real framing.
const ECHO_FRAME_LIMIT: usize = 64 * 1024;

/// What the agent decides and this crate does not.
#[derive(Debug)]
pub struct NodeConfig {
    /// The device key. It is the endpoint id and the peer identity the access rule (#14)
    /// is written against, so it outlives any one process. #6 sources it from the OS
    /// keystore.
    pub secret_key: SecretKey,
    /// Which relays to use. Our own, in production (r3 §11.3); [`RelayMode::Disabled`] in
    /// tests that stay on one machine.
    pub relay_mode: RelayMode,
}

/// A bound endpoint and the task accepting connections on it.
///
/// Dropping a `Node` leaves the endpoint to close on its own schedule. Call
/// [`Node::shutdown`] to close it and wait for the accept task to stop.
#[derive(Debug)]
pub struct Node {
    endpoint: Endpoint,
    accept: JoinHandle<()>,
}

impl Node {
    /// Binds an endpoint on `rfm/1` and starts accepting.
    pub async fn bind(config: NodeConfig) -> Result<Self, BindError> {
        let endpoint = Endpoint::builder(presets::Minimal)
            .secret_key(config.secret_key)
            .relay_mode(config.relay_mode)
            .alpns(vec![ALPN.to_vec()])
            // r3 §11.1. `presets::Minimal` adds no lookup service of its own today; this
            // says so out loud, so a later preset change cannot quietly publish a device
            // key to pkarr. `discovery_is_disabled` in the integration tests is the check.
            .clear_address_lookup()
            .bind()
            .await?;

        info!(id = %endpoint.id().fmt_short(), "endpoint bound");
        let accept = tokio::spawn(accept_loop(endpoint.clone()));
        Ok(Self { endpoint, accept })
    }

    /// This device's endpoint id, which is the public half of its device key.
    pub fn id(&self) -> EndpointId {
        self.endpoint.id()
    }

    /// Where this device can currently be reached. Empty of relay entries until the
    /// endpoint has reached one.
    pub fn addr(&self) -> EndpointAddr {
        self.endpoint.addr()
    }

    /// The bound endpoint. #15 and #18 build on this; it is not part of the seam ADR 0002
    /// describes and callers outside this crate should not need it.
    pub fn endpoint(&self) -> &Endpoint {
        &self.endpoint
    }

    /// Closes the endpoint and waits for the accept task to finish.
    ///
    /// `Endpoint::close` tells peers why the connection went away rather than letting them
    /// time out, and it makes `accept()` return `None`, which is what ends the loop.
    pub async fn shutdown(self) {
        self.endpoint.close().await;
        if let Err(err) = self.accept.await {
            warn!(%err, "accept task did not stop cleanly");
        }
        info!("endpoint closed");
    }
}

async fn accept_loop(endpoint: Endpoint) {
    while let Some(incoming) = endpoint.accept().await {
        tokio::spawn(async move {
            match incoming.await {
                Ok(conn) => {
                    let peer = conn.remote_id();
                    debug!(peer = %peer.fmt_short(), "connection accepted");
                    if let Err(err) = serve(conn).await {
                        debug!(peer = %peer.fmt_short(), %err, "connection ended");
                    }
                }
                // A handshake that never completed. Nothing above the transport has a peer
                // identity to attribute this to, so it stays at debug.
                Err(err) => debug!(%err, "inbound connection failed to establish"),
            }
        });
    }
    debug!("accept loop stopped");
}

/// Placeholder for the file protocol. #15 replaces the body; the connection and stream
/// handling around it is what this issue is for.
async fn serve(conn: Connection) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    loop {
        let (mut send, mut recv) = conn.accept_bi().await?;
        let frame = recv.read_to_end(ECHO_FRAME_LIMIT).await?;
        send.write_all(&frame).await?;
        send.finish()?;
    }
}
