FROM oven/bun:1.3.6-alpine@sha256:819f91180e721ba09e0e5d3eb7fb985832fd23f516e18ddad7e55aaba8100be7 AS web-build

ARG SOURCE_DATE_EPOCH=0
ENV SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH}
WORKDIR /src/web/app
COPY web/app/package.json web/app/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/app/index.html web/app/tsconfig.json web/app/vite.config.ts ./
COPY web/app/public ./public
COPY web/app/src ./src
RUN bun run build

FROM golang:1.25.7-alpine@sha256:f6751d823c26342f9506c03797d2527668d095b0a15f1862cddb4d927a7a4ced AS build

ARG SOURCE_DATE_EPOCH=0
ARG NEROCD_VERSION=0.1.0-dev
ENV SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY db ./db
COPY internal ./internal
COPY web/assets.go ./web/assets.go
COPY web/static ./web/static
COPY --from=web-build /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid= -X main.version=${NEROCD_VERSION}" -o /out/nerocd ./cmd/nerocd

FROM alpine:3.22.2@sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412

# The offline backup/restore subcommands use only these PostgreSQL client
# binaries. They remain behind the explicit Compose `tools` profile and do not
# add a network listener or scheduler to the server process.
RUN apk add --no-cache postgresql17-client \
    && adduser -D -H -u 10001 nerocd
USER nerocd
WORKDIR /app
COPY --from=build /out/nerocd /usr/local/bin/nerocd

EXPOSE 8080
ENTRYPOINT ["nerocd"]
CMD ["server", "--addr", ":8080"]
