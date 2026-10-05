FROM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src
ENV GOTOOLCHAIN=local CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# tools/ocr.go is Go source only: tools/runtime-image and the OCR runtime
# registration compile against it; no Tesseract file enters this image.
COPY tools/media.go tools/runtime.go tools/matroska.go tools/ocr.go tools/manifest.json ./tools/
COPY tools/runtime-image ./tools/runtime-image/
COPY --chmod=0555 .tools/media/linux-amd64/btbn-20260930-9.0/ffmpeg-n9.0.2-17-g2a571b6068-linux64-gpl-9.0/bin/ffprobe /runtime/usr/lib/jelee/ffprobe
COPY --chmod=0444 .tools/media/linux-amd64/btbn-20260930-9.0/ffmpeg-n9.0.2-17-g2a571b6068-linux64-gpl-9.0/LICENSE.txt /runtime/licenses/ffprobe/LICENSE.txt
COPY --chmod=0444 .tools/media-runtime/linux-amd64/debian13-glibc2.41-12deb13u4-gcc14.2.0-19/lib/ /runtime/lib/
COPY --chmod=0555 .tools/media-runtime/linux-amd64/debian13-glibc2.41-12deb13u4-gcc14.2.0-19/lib64/ /runtime/lib64/
COPY --chmod=0444 .tools/media-runtime/linux-amd64/debian13-glibc2.41-12deb13u4-gcc14.2.0-19/licenses/ /runtime/licenses/
# E4 (G37.1): pinned mkvtoolnix 102.0 (mkvmerge, mkvextract; never
# mkvpropedit) and MediaInfo 26.05, installed by make bootstrap-matroska.
# tools/runtime-image below re-verifies every byte against the manifest.
COPY --chmod=0555 .tools/matroska/mkvtoolnix/102.0/linux-amd64/usr/bin/mkvmerge .tools/matroska/mkvtoolnix/102.0/linux-amd64/usr/bin/mkvextract /runtime/usr/lib/jelee/mkvtoolnix/
COPY --chmod=0444 .tools/matroska/mkvtoolnix/102.0/linux-amd64/usr/lib/ /runtime/usr/lib/jelee/mkvtoolnix/lib/
COPY --chmod=0444 .tools/matroska/mkvtoolnix/102.0/linux-amd64/runtime/lib/x86_64-linux-gnu/ /runtime/lib/x86_64-linux-gnu/
COPY --chmod=0444 .tools/matroska/mkvtoolnix/102.0/linux-amd64/runtime/licenses/ /runtime/licenses/runtime/
COPY --chmod=0444 .tools/matroska/mkvtoolnix/102.0/linux-amd64/licenses/ /runtime/licenses/mkvtoolnix/
COPY --chmod=0555 .tools/matroska/mediainfo/26.05/linux-amd64/bin/mediainfo /runtime/usr/lib/jelee/mediainfo
COPY --chmod=0444 .tools/matroska/mediainfo/26.05/linux-amd64/LICENSE /runtime/licenses/mediainfo/LICENSE
RUN find /runtime -type d -exec chmod 0555 {} + && go run ./tools/runtime-image
# Release builds pass --build-arg JELEE_VERSION=x.y.z; an empty or invalid
# value reports buildinfo.DefaultVersion (GET /api/v1/system, OpenAPI).
ARG JELEE_VERSION=
RUN go build -trimpath -ldflags "-X github.com/MoYuanCN/Jelee/internal/platform/buildinfo.version=${JELEE_VERSION}" -o /out/jelee ./cmd/jelee && \
    go build -trimpath -o /out/jelee-cli ./cmd/jelee-cli && \
    go build -trimpath -o /out/jelee-migrate ./cmd/jelee-migrate

FROM scratch
COPY --from=build /runtime/ /
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chmod=0555 /out/ /
COPY --from=build /usr/local/go/LICENSE /licenses/go/LICENSE
COPY --from=build /usr/local/go/PATENTS /licenses/go/PATENTS
COPY LICENSE /LICENSE
COPY docs/LICENSE-COMPLIANCE.md /licenses/README.md
COPY --chmod=0444 internal/adapter/images/LICENSE.x-image /licenses/x-image/LICENSE
COPY --chmod=0444 internal/adapter/legacydb/LICENSE.modernc-sqlite /licenses/modernc-sqlite/LICENSE
COPY --chmod=0444 internal/adapter/legacydb/LICENSE.public-domain-sqlite /licenses/modernc-sqlite/LICENSE-SQLITE
COPY --chmod=0444 internal/adapter/legacydb/LICENSE.modernc-sqlite-third-party.txt /licenses/modernc-sqlite/LICENSE-3RD-PARTY.md
USER 65532:65532
EXPOSE 8097
ENV JELEE_LISTEN=0.0.0.0:8097
# Production image: developer mode (G45) stays off whatever else is set.
# Only a deliberate override of JELEE_ENV re-enables the developer gate.
ENV JELEE_ENV=production
LABEL org.jelee.channel="production"
HEALTHCHECK --interval=30s --timeout=20s --retries=3 CMD ["/jelee-cli", "doctor", "--checks", "config,database,migrations"]
ENTRYPOINT ["/jelee"]
