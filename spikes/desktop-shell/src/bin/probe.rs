//! Stands in for "a peer expects this device to answer". Reads the address the service
//! published, dials it, echoes a payload, prints the round trip. Exit code says it all.

use iroh::endpoint::presets;
use iroh::{Endpoint, EndpointAddr};

const ALPN: &[u8] = b"anywhere-file/spike/echo/0";

#[tokio::main]
async fn main() -> std::process::ExitCode {
    let path = std::env::args()
        .nth(1)
        .unwrap_or_else(|| "/tmp/rfm-spike-headless.json".to_string());
    let addr: EndpointAddr =
        serde_json::from_slice(&std::fs::read(&path).expect("read addr file")).expect("parse addr");
    println!("[probe] dialling {}", addr.id);

    let endpoint = Endpoint::bind(presets::N0).await.expect("bind");
    let t0 = std::time::Instant::now();
    let conn = match endpoint.connect(addr, ALPN).await {
        Ok(c) => c,
        Err(e) => {
            eprintln!("[probe] FAILED to connect: {e}");
            return std::process::ExitCode::FAILURE;
        }
    };
    let (mut send, mut recv) = conn.open_bi().await.expect("open_bi");
    send.write_all(b"is anyone home").await.expect("write");
    send.finish().expect("finish");
    let back = recv.read_to_end(1024).await.expect("read");
    conn.close(0u32.into(), b"bye");
    endpoint.close().await;

    let ok = back == b"is anyone home";
    println!(
        "[probe] {} in {:?}: {:?}",
        if ok { "ECHO OK" } else { "MISMATCH" },
        t0.elapsed(),
        String::from_utf8_lossy(&back)
    );
    if ok {
        std::process::ExitCode::SUCCESS
    } else {
        std::process::ExitCode::FAILURE
    }
}
