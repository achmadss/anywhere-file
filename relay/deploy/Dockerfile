# Relay image: upstream iroh-relay at relay/IROH_VERSION, plus our patches.
#
# Built from the repo root so apply.sh sees IROH_VERSION and patches/:
#
#   docker build -f relay/deploy/Dockerfile -t rfm-relay:<tag> relay/
#
# The context is relay/ on purpose. relay/src/ is gitignored upstream source,
# so apply.sh re-fetches the pinned tag inside the build. What we own stays
# visible as patches/*.patch. See relay/README.md.
ARG RUST_VERSION=1.98.1

FROM rust:${RUST_VERSION}-bookworm AS builder
WORKDIR /build
COPY IROH_VERSION apply.sh ./
COPY patches ./patches
RUN ./apply.sh
# The binary is behind the `server` feature (upstream sets required-features on
# [[bin]]). Without it cargo builds the library, reports success and writes no
# binary, and the failure only surfaces as a missing file in the COPY below.
# `--bin iroh-relay` makes cargo refuse outright if the feature stops enabling it.
RUN cargo build --release --manifest-path src/Cargo.toml -p iroh-relay \
    --features server --bin iroh-relay

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /build/src/target/release/iroh-relay /usr/local/bin/iroh-relay
# 80 (ACME + captive portal), 443 (relay HTTPS + QUIC discovery), 9090 (metrics).
EXPOSE 80 443 9090
ENTRYPOINT ["iroh-relay"]
CMD ["--config-path", "/etc/iroh-relay/relay.toml"]
