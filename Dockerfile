# syntax=docker/dockerfile:1
FROM node:24-bookworm-slim AS web
WORKDIR /build/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.27.2-bookworm AS build
WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=web /build/web/dist/ ./internal/httpapi/assets/
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /smsbot ./cmd/smsbot

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 smsbot && useradd --uid 10001 --gid 10001 --no-create-home smsbot \
    && mkdir -p /var/lib/smsbot /var/lib/smsbot-vault && chown smsbot:smsbot /var/lib/smsbot /var/lib/smsbot-vault
COPY --from=build /smsbot /usr/local/bin/smsbot
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/smsbot/
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/smsbot"]
