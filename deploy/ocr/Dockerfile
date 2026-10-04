# Optional subtitle OCR layer (G15.6). Not part of the default image: it adds
# the pinned Tesseract runtime on top of a runtime image built from the same
# checkout. Build from the repository root:
#
#   make bootstrap-ocr
#   docker build -t jelee:local .
#   docker build -f deploy/ocr/Dockerfile --build-arg JELEE_IMAGE=jelee:local -t jelee:ocr .
#
# tools/runtime-image -ocr verifies every added byte against the manifest;
# nothing from the OCR packages is executed during the build.
ARG JELEE_IMAGE=jelee:local

FROM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS verify
WORKDIR /src
ENV GOTOOLCHAIN=local CGO_ENABLED=0 GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
COPY tools/media.go tools/runtime.go tools/matroska.go tools/ocr.go tools/manifest.json ./tools/
COPY tools/runtime-image ./tools/runtime-image/
COPY --chmod=0555 .tools/ocr/tesseract/5.5.0-1+b1/linux-amd64/bin/tesseract /runtime-ocr/usr/lib/jelee/tesseract/tesseract
COPY --chmod=0444 .tools/ocr/tesseract/5.5.0-1+b1/linux-amd64/lib/ /runtime-ocr/usr/lib/jelee/tesseract/lib/
COPY --chmod=0444 .tools/ocr/tesseract/5.5.0-1+b1/linux-amd64/tessdata/ /runtime-ocr/usr/lib/jelee/tesseract/tessdata/
COPY --chmod=0444 .tools/ocr/tesseract/5.5.0-1+b1/linux-amd64/runtime/lib/x86_64-linux-gnu/libresolv.so.2 /runtime-ocr/lib/x86_64-linux-gnu/libresolv.so.2
COPY --chmod=0444 .tools/ocr/tesseract/5.5.0-1+b1/linux-amd64/licenses/ /runtime-ocr/licenses/tesseract/
RUN find /runtime-ocr -type d -exec chmod 0555 {} + && go run ./tools/runtime-image -ocr

FROM ${JELEE_IMAGE}
COPY --from=verify /runtime-ocr/ /
LABEL org.jelee.optional="subtitle-ocr"
