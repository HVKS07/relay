# Major tags receive security updates. Pin image digests for reproducible releases.
FROM golang:1-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/relay ./cmd/relay && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/backend ./cmd/backend

FROM scratch AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/relay /relay
COPY --from=build /out/backend /backend
COPY config/container.json /config/relay.json
USER 65532:65532
ENTRYPOINT ["/relay"]
CMD ["-config", "/config/relay.json"]
