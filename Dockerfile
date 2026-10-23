# syntax=docker/dockerfile:1.7
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /quadra .

FROM alpine:3.21
RUN apk add --no-cache tzdata && adduser -D -u 10001 app && mkdir /data && chown app:app /data
COPY --from=build /quadra /quadra
USER app
ENV DATABASE_PATH=/data/rankings.db PORT=8080 TZ=America/Sao_Paulo
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/quadra"]
