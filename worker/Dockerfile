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
USER 65532:65532
EXPOSE 8081
ENTRYPOINT ["/app/worker"]
