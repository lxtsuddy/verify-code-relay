FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/relay ./cmd/relay
# distroless 沒有 shell，無法在最終映像 RUN chown：先在這裡建立空的 /data，
# 再以 nonroot (UID 65532) 擁有者複製過去，沒有掛載時也能寫入資料庫。
RUN mkdir -m 0700 /out/data

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/relay /relay
COPY --from=build --chown=65532:65532 /out/data /data
ENV RELAY_CONFIG=/config.yaml
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 CMD ["/relay", "healthcheck"]
ENTRYPOINT ["/relay"]
CMD ["run"]
