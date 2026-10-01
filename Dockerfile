FROM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src
ENV GOTOOLCHAIN=local CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -trimpath -o /out/jelee ./cmd/jelee && \
    go build -trimpath -o /out/jelee-cli ./cmd/jelee-cli && \
    go build -trimpath -o /out/jelee-migrate ./cmd/jelee-migrate

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/ /
COPY --from=build /usr/local/go/LICENSE /licenses/go/LICENSE
COPY --from=build /usr/local/go/PATENTS /licenses/go/PATENTS
COPY LICENSE /LICENSE
COPY docs/LICENSE-COMPLIANCE.md /licenses/README.md
USER 65532:65532
EXPOSE 8097
ENV JELEE_LISTEN=0.0.0.0:8097
HEALTHCHECK --interval=30s --timeout=20s --retries=3 CMD ["/jelee-cli", "doctor"]
ENTRYPOINT ["/jelee"]
