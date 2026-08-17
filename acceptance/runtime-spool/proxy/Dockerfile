FROM golang:1.25.7-alpine@sha256:f6751d823c26342f9506c03797d2527668d095b0a15f1862cddb4d927a7a4ced AS build
WORKDIR /src
COPY main.go ./main.go
RUN CGO_ENABLED=0 go build -trimpath -o /out/runner-api-proxy ./main.go

FROM alpine:3.22.2@sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412
RUN adduser -D -H -u 10002 proxy
USER proxy
COPY --from=build /out/runner-api-proxy /usr/local/bin/runner-api-proxy
ENTRYPOINT ["runner-api-proxy"]
