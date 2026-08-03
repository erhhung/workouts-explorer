FROM docker.io/library/golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api     ./api/cmd/api && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./api/cmd/migrate

FROM docker.io/library/alpine:3.23
RUN apk add --no-cache ca-certificates && \
    addgroup -g 65532 app && \
    adduser -D -H -u 65532 -G app app
COPY --from=build /out/api /out/migrate /app/
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app/api"]
