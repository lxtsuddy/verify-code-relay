FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/relay ./cmd/relay

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/relay /relay
ENV RELAY_CONFIG=/config.yaml
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 CMD ["/relay", "healthcheck"]
ENTRYPOINT ["/relay"]
CMD ["run"]
