# braid-server: the self-hosted team state server. One static binary + SQLite.
# The build stage runs natively and cross-compiles, so multi-arch images don't emulate the Go toolchain.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/braid-server ./cmd/braid-server

FROM alpine:3.22
RUN adduser -D -u 10001 braid && mkdir -p /data && chown braid /data
COPY --from=build /out/braid-server /usr/local/bin/braid-server
USER braid
ENV BRAID_ADDR=:8080 \
    BRAID_DB=/data/braid.db
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["braid-server"]
CMD ["serve"]
