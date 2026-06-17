FROM oven/bun:1.3.6-alpine AS web-build

WORKDIR /src/web/app
COPY web/app/package.json web/app/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/app ./
RUN bun run build

FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY db ./db
COPY internal ./internal
COPY web ./web
COPY --from=web-build /src/web/dist ./web/dist
RUN go build -o /out/nerocd ./cmd/nerocd

FROM alpine:3.22

RUN adduser -D -H -u 10001 nerocd
USER nerocd
WORKDIR /app
COPY --from=build /out/nerocd /usr/local/bin/nerocd

EXPOSE 8080
ENTRYPOINT ["nerocd"]
CMD ["server", "--addr", ":8080"]
