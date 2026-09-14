//! relay-bench: measure sustained relayed throughput against one relay.
//!
//! Two rfm-core endpoints echo frames through the relay for a fixed time and
//! print one JSON line: bytes received, seconds, bytes per second. See
//! relay/deploy/capacity/bench.sh, which runs rounds of this and summarizes.
//!
//!   cargo run --manifest-path relay/fleet/Cargo.toml --bin relay-bench -- \
//!     --relay-url https://relay-eu-west.example.com --seconds 30 --streams 4

use iroh::RelayUrl;

type Res<T = ()> = Result<T, Box<dyn std::error::Error + Send + Sync>>;

fn usage() -> &'static str {
    "usage: relay-bench --relay-url <https-url> [--seconds N] [--streams N]"
}

#[tokio::main]
async fn main() -> Res {
    let mut urls: Vec<RelayUrl> = Vec::new();
    let mut seconds: u64 = 30;
    let mut streams: usize = 4;

    let mut args = std::env::args().skip(1);
    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--relay-url" => {
                let value: String = args.next().expect("--relay-url needs a value");
                urls.push(value.parse().expect("--relay-url must be a URL"));
            }
            "--seconds" => {
                seconds = args
                    .next()
                    .expect("--seconds needs a value")
                    .parse()
                    .expect("--seconds must be a number");
            }
            "--streams" => {
                streams = args
                    .next()
                    .expect("--streams needs a value")
                    .parse()
                    .expect("--streams must be a number");
            }
            _ => {
                eprintln!("{}", usage());
                std::process::exit(2);
            }
        }
    }
    if urls.is_empty() {
        eprintln!("{}", usage());
        std::process::exit(2);
    }

    let mode = rfm_fleet::relay_mode(&urls);
    let iroh::RelayMode::Custom(map) = mode else {
        unreachable!("relay_mode always returns Custom");
    };

    let result = rfm_fleet::bench::bench_throughput(map, seconds, streams).await?;
    println!(
        "{{\"bytes_received\":{},\"seconds\":{:.3},\"bytes_per_second\":{:.0}}}",
        result.bytes_received,
        result.seconds,
        result.bytes_per_second()
    );
    Ok(())
}
