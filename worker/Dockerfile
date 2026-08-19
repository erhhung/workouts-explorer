FROM docker.io/library/golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN for attempt in 1 2 3; do go mod download && exit 0; sleep 5; done; exit 1
COPY worker ./worker
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./worker/cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/osm-migrate ./worker/cmd/osm-migrate && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/osm-catalog ./worker/cmd/osm-catalog

FROM docker.io/library/alpine:3.23
RUN for attempt in 1 2 3; do apk add --no-cache ca-certificates && break; sleep 5; done && \
    test -f /etc/ssl/certs/ca-certificates.crt && \
    addgroup -g 65532 app && \
    adduser -D -H -u 65532 -G app app
COPY --from=build /out/worker /app/worker
COPY --from=build /out/osm-migrate /app/osm-migrate
COPY --from=build /out/osm-catalog /app/osm-catalog
USER 65532:65532
EXPOSE 8081
ENTRYPOINT ["/app/worker"]
