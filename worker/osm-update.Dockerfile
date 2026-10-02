FROM docker.io/library/golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY worker/cmd/osm-update ./worker/cmd/osm-update
COPY worker/cmd/osm-identity-eval ./worker/cmd/osm-identity-eval
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/osm-update ./worker/cmd/osm-update && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/osm-identity-eval ./worker/cmd/osm-identity-eval

FROM --platform=linux/amd64 docker.io/iboates/osmium:1.19.0@sha256:65b753a3df8aa693e369b9d776e980337ac50c4f9d19514f26b100c9d1d249f5 AS osmium

FROM --platform=linux/amd64 docker.io/iboates/osm2pgsql:2.3.1@sha256:25ad3e2c316f4c582f188c88d50bdae4b7d67671ade99c9a7539d2c2a2c0a670
USER root
RUN for attempt in 1 2 3; do apk add --no-cache ca-certificates postgresql18-client tzdata && break; sleep 5; done && \
    apk info --exists postgresql18-client && \
    addgroup -g 65532 app && adduser -D -H -u 65532 -G app app && \
    install -d -o 65532 -g 65532 -m 0700 /scratch && \
    install -d -o 65532 -g 65532 -m 0755 /app && \
    install -o 65532 -g 65532 -m 0444 /dev/null /app/osm-operator-v1
COPY --from=osmium /usr/local/bin/osmium /usr/local/bin/osmium
COPY --from=build /out/osm-update /app/osm-update
COPY --from=build /out/osm-identity-eval /app/osm-identity-eval
COPY osm /app/osm
USER 65532:65532
ENV OSM_PIPELINE_ROOT=/app/osm OSM_UPDATE_SCRATCH=/scratch
ENTRYPOINT ["/app/osm-update"]
