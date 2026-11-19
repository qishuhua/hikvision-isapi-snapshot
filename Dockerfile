FROM golang:1.27.4-alpine3.24 AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /snapshot-server ./cmd/server

FROM alpine:3.24
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S snapshot \
    && adduser -S -G snapshot snapshot \
    && mkdir -p /app/data \
    && chown snapshot:snapshot /app/data
WORKDIR /app
COPY --from=build /snapshot-server /app/snapshot-server
USER snapshot
ENV HTTP_ADDR=:8080 DATA_PATH=/app/data/state.json
EXPOSE 8080
ENTRYPOINT ["/app/snapshot-server"]
