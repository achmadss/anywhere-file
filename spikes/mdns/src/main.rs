//! Throwaway harness for issue #3: does a pure-Rust DNS-SD responder give us
//! `_rfm._tcp` with the TXT records r3 §11.1 wants, on a LAN with no Internet,
//! and how fast is discovery?
//!
//! Three subcommands, each meant to run as its own process:
//!
//!   advertise <dev> <trust-list-version> <seconds> <ws>...
//!       Registers one `_rfm._tcp.local.` instance per workspace id.
//!   browse <expected-count> <timeout-ms>
//!       Browses and prints elapsed ms from process start to each resolution.
//!   bind5353
//!       Minimal raw multicast bind and query send, to see what the OS asks the user.
//!
//! Exit code 0 from `browse` means the expected count resolved before the timeout.

use std::collections::HashMap;
use std::net::{Ipv4Addr, SocketAddr, UdpSocket};
use std::process;
use std::time::{Duration, Instant};

use mdns_sd::{ServiceDaemon, ServiceEvent, ServiceInfo};

const TY: &str = "_rfm._tcp.local.";
const MDNS_GROUP: Ipv4Addr = Ipv4Addr::new(224, 0, 0, 251);

fn main() {
    let started = Instant::now();
    let args: Vec<String> = std::env::args().collect();
    let rest = if args.len() > 2 { &args[2..] } else { &[] };
    match args.get(1).map(String::as_str) {
        Some("advertise") => advertise(rest),
        Some("browse") => browse(rest, started),
        Some("bind5353") => bind5353(),
        _ => {
            eprintln!(
                "usage:\n  {0} advertise <dev> <v> <seconds> <ws>...\n  {0} browse <expect> <timeout-ms>\n  {0} bind5353",
                args[0]
            );
            process::exit(2);
        }
    }
}

fn advertise(args: &[String]) {
    let dev = &args[0];
    let version = &args[1];
    let seconds: u64 = args[2].parse().expect("seconds");
    let workspaces = &args[3..];
    assert!(!workspaces.is_empty(), "need at least one workspace id");

    let mdns = ServiceDaemon::new().expect("daemon");
    let host = format!("rfm-{dev}.local.");

    for ws in workspaces {
        let txt: HashMap<String, String> = HashMap::from([
            ("ws".to_string(), ws.clone()),
            ("dev".to_string(), dev.clone()),
            ("v".to_string(), version.clone()),
        ]);
        // One instance per (device, workspace) pair. r3 §11.1: a device in several
        // workspaces advertises one record per workspace.
        let instance = format!("{dev}-{ws}");
        let info = ServiceInfo::new(TY, &instance, &host, (), 4433, txt)
            .expect("service info")
            .enable_addr_auto();
        mdns.register(info).expect("register");
        println!("registered {instance}.{TY} ws={ws} dev={dev} v={version}");
    }

    std::thread::sleep(Duration::from_secs(seconds));
    let _ = mdns.shutdown();
}

fn browse(args: &[String], started: Instant) {
    let expect: usize = args[0].parse().expect("expect");
    let timeout = Duration::from_millis(args[1].parse().expect("timeout-ms"));

    let mdns = ServiceDaemon::new().expect("daemon");
    let rx = mdns.browse(TY).expect("browse");

    let mut seen: Vec<String> = Vec::new();
    let deadline = Instant::now() + timeout;
    while seen.len() < expect {
        let left = deadline.saturating_duration_since(Instant::now());
        if left.is_zero() {
            break;
        }
        match rx.recv_timeout(left) {
            Ok(ServiceEvent::ServiceResolved(r)) => {
                if seen.contains(&r.fullname) {
                    continue;
                }
                let txt = |k: &str| r.get_property_val_str(k).unwrap_or("<missing>").to_string();
                let addrs: Vec<String> = r.addresses.iter().map(|a| a.to_string()).collect();
                println!(
                    "RESOLVED ms={} name={} ws={} dev={} v={} port={} addrs={}",
                    started.elapsed().as_millis(),
                    r.fullname,
                    txt("ws"),
                    txt("dev"),
                    txt("v"),
                    r.port,
                    addrs.join(",")
                );
                seen.push(r.fullname.clone());
            }
            Ok(_) => {}
            Err(_) => break,
        }
    }

    let _ = mdns.shutdown();
    if seen.len() < expect {
        eprintln!(
            "TIMEOUT after {} ms, resolved {}/{expect}",
            started.elapsed().as_millis(),
            seen.len()
        );
        process::exit(1);
    }
}

/// The smallest thing that makes the OS notice we want the local network: bind
/// UDP 5353, join 224.0.0.251, send one PTR query for `_rfm._tcp.local.`.
fn bind5353() {
    // First the naive bind, because what it does on a machine that already runs
    // mDNSResponder or avahi-daemon is worth knowing.
    match UdpSocket::bind(SocketAddr::from(([0, 0, 0, 0], 5353))) {
        Ok(_) => println!("plain bind 0.0.0.0:5353 ok (nothing else holds the port)"),
        Err(e) => println!("plain bind 0.0.0.0:5353 FAILED: {e} (kind {:?})", e.kind()),
    }

    // Then the way a responder has to do it: SO_REUSEADDR and SO_REUSEPORT first.
    let sock = {
        let s = socket2::Socket::new(socket2::Domain::IPV4, socket2::Type::DGRAM, None)
            .expect("socket");
        s.set_reuse_address(true).expect("SO_REUSEADDR");
        // socket2 only exposes SO_REUSEPORT on unix, and Winsock has no equivalent:
        // on Windows SO_REUSEADDR alone is what lets a second responder bind 5353.
        // mdns-sd guards the same call the same way (service_daemon.rs:927).
        #[cfg(unix)]
        if let Err(e) = s.set_reuse_port(true) {
            println!("SO_REUSEPORT unsupported: {e}");
        }
        match s.bind(&SocketAddr::from(([0, 0, 0, 0], 5353)).into()) {
            Ok(()) => println!("reuse bind 0.0.0.0:5353 ok"),
            Err(e) => {
                println!("reuse bind 0.0.0.0:5353 FAILED: {e}");
                process::exit(1);
            }
        }
        UdpSocket::from(s)
    };
    match sock.join_multicast_v4(&MDNS_GROUP, &Ipv4Addr::UNSPECIFIED) {
        Ok(()) => println!("join 224.0.0.251 ok"),
        Err(e) => println!("join 224.0.0.251 FAILED: {e}"),
    }
    // Hand-rolled DNS query: id 0, flags 0, one question, PTR/IN for _rfm._tcp.local.
    let mut q = vec![0u8, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0];
    for label in ["_rfm", "_tcp", "local"] {
        q.push(label.len() as u8);
        q.extend_from_slice(label.as_bytes());
    }
    q.extend_from_slice(&[0, 0, 12, 0, 1]);
    match sock.send_to(&q, (MDNS_GROUP, 5353)) {
        Ok(n) => println!("sent {n} bytes to 224.0.0.251:5353"),
        Err(e) => println!("send FAILED: {e}"),
    }
    sock.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let mut buf = [0u8; 1500];
    match sock.recv_from(&mut buf) {
        Ok((n, from)) => println!("received {n} bytes from {from}"),
        Err(e) => println!("no reply within 3s: {e}"),
    }
}
