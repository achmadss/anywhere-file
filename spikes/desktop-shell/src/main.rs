//! Spike for issue #4: a Tauri window that hosts an iroh endpoint in the same process,
//! and two ways of getting a multi-GB file from Rust into the webview.
//!
//! Runtime ownership: we build the tokio runtime ourselves and hand it to Tauri with
//! `tauri::async_runtime::set`. The tao/AppKit event loop owns the main thread and is not
//! async; everything async (iroh, the localhost streamer, the uri-scheme handler) runs on
//! that one tokio runtime's worker threads.

use std::io::SeekFrom;
use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};

use iroh::endpoint::{Connection, presets};
use iroh::protocol::{AcceptError, ProtocolHandler, Router};
use iroh::{Endpoint, EndpointAddr};
use tauri::Emitter;
use tokio::io::{AsyncReadExt, AsyncSeekExt, AsyncWriteExt};

const ALPN: &[u8] = b"anywhere-file/spike/echo/0";

/// Cap on a single custom-protocol response body. Tauri's own asset protocol uses
/// 1000*1024 for the same reason (tauri-2.11.5/src/protocol/asset.rs:118): the responder
/// takes a fully materialised `Cow<[u8]>`, so whatever you answer with is resident.
const PROTOCOL_CHUNK: u64 = 8 * 1024 * 1024;

/// Copy buffer for the localhost streamer. This is the entire steady-state cost of
/// sending a file of any size down that path.
const STREAM_BUF: usize = 256 * 1024;

struct State {
    endpoint_id: Mutex<String>,
    endpoint_addr: Mutex<String>,
    stream_file: Mutex<PathBuf>,
    http_port: AtomicU64,
}

#[derive(Debug, Clone)]
struct Echo;

impl ProtocolHandler for Echo {
    async fn accept(&self, connection: Connection) -> Result<(), AcceptError> {
        let peer = connection.remote_id();
        eprintln!("[iroh] accepted connection from {peer}");
        let (mut send, mut recv) = connection.accept_bi().await?;
        let n = tokio::io::copy(&mut recv, &mut send).await?;
        send.finish()?;
        connection.closed().await;
        eprintln!("[iroh] echoed {n} bytes back to {peer}");
        Ok(())
    }
}

// ---------------------------------------------------------------- IPC commands

#[tauri::command]
fn endpoint_id(state: tauri::State<'_, Arc<State>>) -> String {
    state.endpoint_id.lock().unwrap().clone()
}

#[tauri::command]
fn endpoint_addr(state: tauri::State<'_, Arc<State>>) -> String {
    state.endpoint_addr.lock().unwrap().clone()
}

#[tauri::command]
fn http_port(state: tauri::State<'_, Arc<State>>) -> u64 {
    state.http_port.load(Ordering::Relaxed)
}

/// The one directory listing the acceptance criteria ask for. Names and sizes only.
#[tauri::command]
fn list_dir(path: String) -> Result<Vec<(String, u64)>, String> {
    let mut out = Vec::new();
    for entry in std::fs::read_dir(&path).map_err(|e| e.to_string())? {
        let entry = entry.map_err(|e| e.to_string())?;
        let len = entry.metadata().map(|m| m.len()).unwrap_or(0);
        out.push((entry.file_name().to_string_lossy().into_owned(), len));
    }
    out.sort();
    Ok(out)
}

#[tauri::command]
fn set_stream_file(path: String, state: tauri::State<'_, Arc<State>>) -> Result<u64, String> {
    let p = PathBuf::from(path);
    let len = std::fs::metadata(&p).map_err(|e| e.to_string())?.len();
    *state.stream_file.lock().unwrap() = p;
    Ok(len)
}

/// Resident set size of this process in KiB, straight from `ps`, so the number in the
/// window is the same number an outside sampler sees.
#[tauri::command]
fn rss_kb() -> u64 {
    let pid = std::process::id();
    std::process::Command::new("ps")
        .args(["-o", "rss=", "-p", &pid.to_string()])
        .output()
        .ok()
        .and_then(|o| String::from_utf8_lossy(&o.stdout).trim().parse().ok())
        .unwrap_or(0)
}

/// The webview reports its progress back so it lands in the same log as the RSS samples.
#[tauri::command]
fn mark(label: String, bytes: u64) {
    println!("[mark] {label} bytes={bytes} rss_kb={}", rss_kb());
}

// ---------------------------------------------------------------- localhost streamer

/// A hand-rolled HTTP/1.1 responder. It exists to answer one question: can the webview
/// pull a body larger than memory. `tokio::io::copy` with a fixed buffer is the whole
/// mechanism; there is no framework here on purpose.
async fn serve_http(listener: tokio::net::TcpListener, state: Arc<State>) {
    loop {
        let Ok((mut sock, _)) = listener.accept().await else {
            continue;
        };
        let state = state.clone();
        tokio::spawn(async move {
            // Read past the request head. We do not care what it asked for.
            let mut head = Vec::new();
            let mut byte = [0u8; 1];
            while head.len() < 8192 {
                match sock.read(&mut byte).await {
                    Ok(0) | Err(_) => return,
                    Ok(_) => head.push(byte[0]),
                }
                if head.ends_with(b"\r\n\r\n") {
                    break;
                }
            }

            let path = state.stream_file.lock().unwrap().clone();
            let Ok(mut file) = tokio::fs::File::open(&path).await else {
                let _ = sock
                    .write_all(b"HTTP/1.1 404 Not Found\r\ncontent-length: 0\r\n\r\n")
                    .await;
                return;
            };
            let len = file.metadata().await.map(|m| m.len()).unwrap_or(0);

            let head = format!(
                "HTTP/1.1 200 OK\r\ncontent-type: application/octet-stream\r\ncontent-length: {len}\r\naccess-control-allow-origin: *\r\nconnection: close\r\n\r\n"
            );
            if sock.write_all(head.as_bytes()).await.is_err() {
                return;
            }

            let mut buf = vec![0u8; STREAM_BUF];
            let mut sent = 0u64;
            loop {
                let n = match file.read(&mut buf).await {
                    Ok(0) | Err(_) => break,
                    Ok(n) => n,
                };
                if sock.write_all(&buf[..n]).await.is_err() {
                    break;
                }
                sent += n as u64;
            }
            let _ = sock.shutdown().await;
            println!("[http] streamed {sent} bytes of {len} rss_kb={}", rss_kb());
        });
    }
}

// ---------------------------------------------------------------- entry point

fn main() {
    // One runtime for the whole process, ours, handed to Tauri.
    let rt = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .expect("tokio runtime");
    tauri::async_runtime::set(rt.handle().clone());

    let big = std::env::var("SPIKE_BIG_FILE").unwrap_or_else(|_| "/dev/null".to_string());
    let run = std::env::var("SPIKE_RUN").unwrap_or_default();
    let dir = std::env::var("SPIKE_DIR").unwrap_or_else(|_| "/".to_string());

    let state = Arc::new(State {
        endpoint_id: Mutex::new("(binding)".into()),
        endpoint_addr: Mutex::new(String::new()),
        stream_file: Mutex::new(PathBuf::from(&big)),
        http_port: AtomicU64::new(0),
    });

    // Localhost streamer. Bound before the window so the port is known when JS asks.
    let listener = rt
        .block_on(tokio::net::TcpListener::bind("127.0.0.1:0"))
        .expect("bind localhost streamer");
    let port = listener.local_addr().unwrap().port();
    state.http_port.store(port as u64, Ordering::Relaxed);
    println!("[http] listening on 127.0.0.1:{port}");
    rt.spawn(serve_http(listener, state.clone()));

    let protocol_state = state.clone();
    let ipc_state = state.clone();

    tauri::Builder::default()
        .manage(state.clone())
        .register_asynchronous_uri_scheme_protocol("rfmfile", move |_ctx, request, responder| {
            let state = protocol_state.clone();
            // Runs on Tauri's runtime, which is the runtime we set above.
            tauri::async_runtime::spawn(async move {
                responder.respond(serve_range(&state, &request).await);
            });
        })
        .invoke_handler(tauri::generate_handler![
            endpoint_id,
            endpoint_addr,
            http_port,
            list_dir,
            set_stream_file,
            rss_kb,
            mark
        ])
        .setup(move |app| {
            let handle = app.handle().clone();
            let st = ipc_state.clone();
            // iroh binds on the same runtime the UI thread just handed off to.
            tauri::async_runtime::spawn(async move {
                match Endpoint::bind(presets::N0).await {
                    Ok(ep) => {
                        let id = ep.id().to_string();
                        println!("[iroh] endpoint id {id}");
                        *st.endpoint_id.lock().unwrap() = id.clone();
                        let router = Router::builder(ep).accept(ALPN, Echo).spawn();
                        router.endpoint().online().await;
                        let addr: EndpointAddr = router.endpoint().addr();
                        let addr_json = serde_json::to_string(&addr).unwrap_or_default();
                        *st.endpoint_addr.lock().unwrap() = addr_json.clone();
                        println!("[iroh] online, addr {addr_json}");
                        let _ = handle.emit("endpoint-ready", id);
                        if let Ok(f) = std::env::var("SPIKE_ADDR_FILE") {
                            let _ = std::fs::write(&f, &addr_json);
                            println!("[iroh] wrote {f}");
                        }
                        // Hold the router, and so the endpoint, for the life of the window.
                        std::future::pending::<()>().await;
                        drop(router);
                    }
                    Err(e) => {
                        eprintln!("[iroh] bind failed: {e}");
                        *st.endpoint_id.lock().unwrap() = format!("bind failed: {e}");
                    }
                }
            });
            // The window is built here, not in tauri.conf.json, so the query string can
            // carry the scripted run. `?run=list,http,proto` drives the whole spike.
            let url = format!("index.html?run={run}&dir={dir}");
            let win =
                tauri::WebviewWindowBuilder::new(app, "main", tauri::WebviewUrl::App(url.into()))
                    .title("anywhere-file desktop shell spike")
                    .inner_size(980.0, 760.0)
                    .build()?;
            println!(
                "[win] visible={:?} inner={:?} outer_pos={:?} scale={:?}",
                win.is_visible(),
                win.inner_size(),
                win.outer_position(),
                win.scale_factor()
            );
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("tauri run");
}

/// Custom protocol handler with Range support. A response body here is a `Vec<u8>` that
/// Tauri hands whole to WKWebView, so the only way this stays bounded is to refuse to
/// answer with more than `PROTOCOL_CHUNK` at a time.
async fn serve_range(
    state: &State,
    request: &tauri::http::Request<Vec<u8>>,
) -> tauri::http::Response<Vec<u8>> {
    let path = state.stream_file.lock().unwrap().clone();
    let Ok(mut file) = tokio::fs::File::open(&path).await else {
        return tauri::http::Response::builder()
            .status(404)
            .body(Vec::new())
            .unwrap();
    };
    let len = file.metadata().await.map(|m| m.len()).unwrap_or(0);

    // SPIKE_NO_CAP exists to prove the cap is what keeps memory flat: without it a single
    // unranged GET answers with the whole file and RSS tracks the file size.
    let cap = if std::env::var("SPIKE_NO_CAP").is_ok() {
        u64::MAX
    } else {
        PROTOCOL_CHUNK
    };

    let range = request
        .headers()
        .get("range")
        .and_then(|v| v.to_str().ok())
        .and_then(parse_range);

    let (start, end) = match range {
        Some((s, e)) => (s.min(len), e.unwrap_or(len - 1).min(len - 1)),
        None => (0, (cap.saturating_sub(1)).min(len.saturating_sub(1))),
    };
    let end = end.min(start.saturating_add(cap.saturating_sub(1)));
    let want = (end - start + 1) as usize;

    if file.seek(SeekFrom::Start(start)).await.is_err() {
        return tauri::http::Response::builder()
            .status(500)
            .body(Vec::new())
            .unwrap();
    }
    let mut buf = vec![0u8; want];
    let mut filled = 0usize;
    while filled < want {
        match file.read(&mut buf[filled..]).await {
            Ok(0) | Err(_) => break,
            Ok(n) => filled += n,
        }
    }
    buf.truncate(filled);

    tauri::http::Response::builder()
        .status(206)
        .header("content-type", "application/octet-stream")
        .header("accept-ranges", "bytes")
        .header(
            "content-range",
            format!("bytes {start}-{}/{len}", start + filled as u64 - 1),
        )
        .header("access-control-allow-origin", "*")
        .header("access-control-expose-headers", "content-range")
        .body(buf)
        .unwrap()
}

fn parse_range(h: &str) -> Option<(u64, Option<u64>)> {
    let rest = h.strip_prefix("bytes=")?;
    let (a, b) = rest.split_once('-')?;
    let start = a.trim().parse().ok()?;
    let end = b.trim();
    Some((
        start,
        if end.is_empty() {
            None
        } else {
            end.parse().ok()
        },
    ))
}
