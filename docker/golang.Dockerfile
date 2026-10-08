# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:24.12.0-bookworm-slim@sha256:7326fb2dbdce998edd72140946851be64ef4a643e8715e138ca467e8e9d92c99 AS ui
WORKDIR /workspace/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci --no-fund
COPY ui/ ./
COPY docs/openapi.yaml /workspace/docs/openapi.yaml
COPY src/config/settings.go /workspace/src/config/settings.go
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26.6@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6 AS build
WORKDIR /src
COPY src/go.mod src/go.sum ./
RUN go mod download
COPY src/ ./
COPY --from=ui /workspace/src/ui/web/dist ./ui/web/dist
ARG TARGETOS=linux
ARG TARGETARCH
RUN mkdir -p /runtime/storages && touch /runtime/storages/.keep
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags purego -trimpath -ldflags='-s -w' -o /gobale .

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG VERSION=2.1.0
ARG REVISION=unknown
LABEL org.opencontainers.image.title="GoBale" \
      org.opencontainers.image.description="Native Go multi-account Bale gateway with REST APIs and durable webhooks" \
      org.opencontainers.image.source="https://github.com/mimalef70/gobale" \
      org.opencontainers.image.url="https://mimalef70.github.io/gobale/" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$REVISION
WORKDIR /app
COPY --from=build --chown=65532:65532 /gobale /app/gobale
COPY --from=build --chown=65532:65532 /runtime/storages /app/storages
COPY --chown=65532:65532 LICENCE.txt /app/
COPY --from=ui --chown=65532:65532 /workspace/src/ui/web/dist/THIRD_PARTY_NOTICES.txt /app/
ENV APP_HOST=0.0.0.0 APP_PORT=3000 APP_DATABASE=/app/storages/gobale.db APP_MEDIA_ROOT=/app/storages/media
VOLUME ["/app/storages"]
EXPOSE 3000
USER 65532:65532
ENTRYPOINT ["/app/gobale"]
CMD ["rest"]
