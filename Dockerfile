# --- Builder stage (Debian bookworm) ---
FROM golang:1.25-bookworm AS builder

# Install build dependencies including libolm
RUN apt-get update -y \
    && apt-get install -y --no-install-recommends git ca-certificates build-essential libolm-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Build with CGO to link against libolm
RUN CGO_ENABLED=1 go build -o matrimail ./cmd/matrimail

# Prepare a data directory we can chown in final image via COPY --chown
RUN mkdir -p /runtime-data

# --- Runtime dependencies stage (Debian bookworm-slim) ---
# Stage layout: install libolm + ca-certs into a known prefix, then COPY that
# whole prefix into the distroless final stage. This avoids hardcoding the
# arch-specific multiarch path (x86_64-linux-gnu vs aarch64-linux-gnu) and
# lets buildx produce a working image for both linux/amd64 and linux/arm64.
FROM debian:bookworm-slim AS runtime-deps
RUN apt-get update -y \
    && apt-get install -y --no-install-recommends ca-certificates libolm3 tzdata \
    && rm -rf /var/lib/apt/lists/*

# Stage the libolm shared object under an arch-agnostic path so the COPY in
# the final stage doesn't need to know the target arch.
RUN mkdir -p /matrimail-runtime/lib /matrimail-runtime/etc/ssl/certs /matrimail-runtime/usr/share \
    && cp -L "$(find /usr/lib -name libolm.so.3 -type f | head -n1)" /matrimail-runtime/lib/libolm.so.3 \
    && cp /etc/ssl/certs/ca-certificates.crt /matrimail-runtime/etc/ssl/certs/ca-certificates.crt \
    && cp -r /usr/share/zoneinfo /matrimail-runtime/usr/share/zoneinfo

# --- Final minimal runtime (Distroless) ---
# Distroless base matching Debian 12
FROM gcr.io/distroless/cc-debian12:nonroot

# Copy the compiled binary
COPY --from=builder /build/matrimail /usr/bin/matrimail
# Copy required shared libraries and data from runtime-deps. We place libolm
# at /usr/lib (not /usr/lib/<arch>-linux-gnu) so the dynamic linker finds it
# regardless of architecture — distroless's /etc/ld.so.conf.d already
# searches /usr/lib.
COPY --from=runtime-deps /matrimail-runtime/lib/libolm.so.3 /usr/lib/libolm.so.3
COPY --from=runtime-deps /matrimail-runtime/etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=runtime-deps /matrimail-runtime/usr/share/zoneinfo /usr/share/zoneinfo

# Use a path owned by the nonroot user in distroless
WORKDIR /home/nonroot/app
# Ensure a writable data directory owned by the nonroot user exists
COPY --from=builder --chown=nonroot:nonroot /runtime-data /home/nonroot/app/data
# Expose a writable volume for data (mount a host volume here)
VOLUME ["/home/nonroot/app/data"]

EXPOSE 29319
ENTRYPOINT ["/usr/bin/matrimail"]
