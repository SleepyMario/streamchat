FROM golang:1.24-bookworm AS build

ARG VERSION
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN test -n "$VERSION" && \
    CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags "-s -w -X main.version=$VERSION" \
      -o /out/streamchat \
      ./cmd/streamchat

FROM debian:bookworm-slim AS runtime

ARG VERSION
LABEL org.opencontainers.image.source="https://github.com/SleepyMario/streamchat" \
      org.opencontainers.image.version="$VERSION"

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tzdata && \
    rm -rf /var/lib/apt/lists/* && \
    groupadd --system streamchat && \
    useradd --system --gid streamchat --home-dir /var/lib/streamchat streamchat && \
    install -d -o streamchat -g streamchat -m 0750 /var/lib/streamchat

COPY --from=build /out/streamchat /usr/local/bin/streamchat

USER streamchat:streamchat
WORKDIR /var/lib/streamchat
VOLUME ["/var/lib/streamchat"]

ENTRYPOINT ["/usr/local/bin/streamchat"]

FROM runtime AS cli

LABEL org.opencontainers.image.title="Streamchat CLI" \
      org.opencontainers.image.description="Interactive multi-platform streaming chat client"

CMD ["run"]

FROM runtime AS server

LABEL org.opencontainers.image.title="Streamchat Server" \
      org.opencontainers.image.description="Headless multi-platform chat ingestion, archive, and relay server"

VOLUME ["/etc/streamchat"]
CMD ["serve", "--config", "/etc/streamchat/config.json"]
