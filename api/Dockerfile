FROM docker.io/library/golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN for attempt in 1 2 3; do go mod download && exit 0; sleep 5; done; exit 1
COPY api ./api
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api     ./api/cmd/api && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./api/cmd/migrate && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bootstrap-admin ./api/cmd/bootstrap-admin && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/provision-roles ./api/cmd/provision-roles

FROM docker.io/library/alpine:3.23
RUN for attempt in 1 2 3; do apk add --no-cache ca-certificates && break; sleep 5; done && \
    test -f /etc/ssl/certs/ca-certificates.crt && \
    addgroup -g 65532 app && \
    adduser -D -H -u 65532 -G app app
COPY --from=build /out/api /out/migrate /out/bootstrap-admin /out/provision-roles /app/
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app/api"]
