//! The service half of the "user-level service plus a windowed app" model (r3 §13.1).
//! No window, no webview, no AppKit. It binds an iroh endpoint, writes its address where
//! a peer can find it, and answers forever. This is what launchd keeps alive.

use std::path::PathBuf;

use iroh::endpoint::{Connection, presets};
use iroh::protocol::{AcceptError, ProtocolHandler, Router};
use iroh::{Endpoint, EndpointAddr};

const ALPN: &[u8] = b"anywhere-file/spike/echo/0";

#[derive(Debug, Clone)]
struct Echo;

impl ProtocolHandler for Echo {
    async fn accept(&self, connection: Connection) -> Result<(), AcceptError> {
        let peer = connection.remote_id();
        println!("[headless] accepted from {peer}");
        let (mut send, mut recv) = connection.accept_bi().await?;
        let n = tokio::io::copy(&mut recv, &mut send).await?;
        send.finish()?;
        connection.closed().await;
        println!("[headless] echoed {n} bytes to {peer}");
        Ok(())
    }
}

#[tokio::main]
async fn main() {
    let out: PathBuf = std::env::var("SPIKE_ADDR_FILE")
        .unwrap_or_else(|_| "/tmp/rfm-spike-headless.json".into())
        .into();

    let endpoint = Endpoint::bind(presets::N0).await.expect("bind endpoint");
    println!(
        "[headless] pid {} endpoint id {}",
        std::process::id(),
        endpoint.id()
    );

    let router = Router::builder(endpoint).accept(ALPN, Echo).spawn();
    router.endpoint().online().await;

    let addr: EndpointAddr = router.endpoint().addr();
    std::fs::write(&out, serde_json::to_vec(&addr).unwrap()).expect("write addr file");
    println!("[headless] online, wrote {} ", out.display());

    // Nothing to do but be reachable.
    std::future::pending::<()>().await;
}
