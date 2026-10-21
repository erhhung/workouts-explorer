FROM docker.io/library/alpine:3.23 AS rclone
RUN for attempt in 1 2 3; do apk add --no-cache ca-certificates unzip && break; sleep 5; done && \
    apk info --exists ca-certificates unzip
RUN set -eu; \
    case "$(uname -m)" in \
      x86_64) arch=amd64; checksum=349ac8fba6ff65d6247043f1750cdcb518ec5d500ef91463a10d37c0ccdf3702 ;; \
      aarch64) arch=arm64; checksum=7e1e8d69654941b7b7df84ee74c5f7fb09cce7d5496947fb7bf16b55ff427d10 ;; \
      *) exit 1 ;; \
    esac; \
    archive="rclone-v1.75.2-linux-${arch}"; \
    wget -q -T 60 -O /tmp/rclone.zip "https://downloads.rclone.org/v1.75.2/${archive}.zip"; \
    printf '%s  /tmp/rclone.zip\n' "$checksum" | sha256sum -c -; \
    unzip -q /tmp/rclone.zip "${archive}/rclone" -d /tmp; \
    cp "/tmp/${archive}/rclone" /usr/local/bin/rclone; \
    chmod 0755 /usr/local/bin/rclone; \
    rclone --config /dev/null version

FROM docker.io/library/golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN for attempt in 1 2 3; do go mod download && exit 0; sleep 5; done; exit 1
COPY worker ./worker
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./worker/cmd/worker && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/coverage-worker ./worker/cmd/coverage-worker && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/osm-migrate ./worker/cmd/osm-migrate && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/osm-catalog ./worker/cmd/osm-catalog && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/timezone-import ./worker/cmd/timezone-import && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/timezone-backfill ./worker/cmd/timezone-backfill && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/coverage-evaluate ./worker/cmd/coverage-evaluate

FROM docker.io/library/alpine:3.23
RUN for attempt in 1 2 3; do apk add --no-cache ca-certificates && break; sleep 5; done && \
    apk info --exists ca-certificates && \
    test -f /etc/ssl/certs/ca-certificates.crt && \
    addgroup -g 65532 app && \
    adduser -D -H -u 65532 -G app app
COPY --from=build /out/worker /app/worker
COPY --from=build /out/coverage-worker /app/coverage-worker
COPY --from=build /out/osm-migrate /app/osm-migrate
COPY --from=build /out/osm-catalog /app/osm-catalog
COPY --from=build /out/timezone-import /app/timezone-import
COPY --from=build /out/timezone-backfill /app/timezone-backfill
COPY --from=build /out/coverage-evaluate /app/coverage-evaluate
COPY --from=rclone /usr/local/bin/rclone /usr/local/bin/rclone
USER 65532:65532
EXPOSE 8081
ENTRYPOINT ["/app/worker"]
