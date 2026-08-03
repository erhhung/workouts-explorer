FROM docker.io/library/golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY worker ./worker
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./worker/cmd/worker

FROM docker.io/library/alpine:3.23
RUN apk add --no-cache ca-certificates && \
    addgroup -g 65532 app && \
    adduser -D -H -u 65532 -G app app
COPY --from=build /out/worker /app/worker
USER 65532:65532
EXPOSE 8081
ENTRYPOINT ["/app/worker"]
