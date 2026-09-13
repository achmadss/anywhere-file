# Two Linux images for the issue #3 spike: the same binary with and without a
# running avahi-daemon, so we can see whether the embedded responder needs the
# platform one and whether the two can share UDP 5353.
#
#   docker build --target noavahi -t rfm-mdns:noavahi spikes/mdns
#   docker build --target avahi   -t rfm-mdns:avahi   spikes/mdns

FROM rust:1.98-bookworm AS build
WORKDIR /src
COPY Cargo.toml ./
COPY src ./src
RUN cargo build --release

FROM debian:bookworm-slim AS noavahi
RUN apt-get update \
 && apt-get install -y --no-install-recommends iproute2 iputils-ping \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /src/target/release/rfm-mdns-spike /usr/local/bin/rfm-mdns-spike

FROM noavahi AS avahi
RUN apt-get update \
 && apt-get install -y --no-install-recommends avahi-daemon avahi-utils dbus \
 && rm -rf /var/lib/apt/lists/*
# avahi refuses to start with the default host-name check on a container, and it
# wants dbus up first. Started by hand in the test script, not by an entrypoint.
