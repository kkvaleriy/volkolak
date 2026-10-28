# syntax=docker/dockerfile:1

# whisper.cpp собирается здесь же, а не копируется готовым бинарником:
# так он линкуется с glibc этого образа и без -march родного CPU сборщика.
FROM debian:bookworm-slim AS whisper-build
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential ca-certificates cmake git \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone --depth 1 --branch v1.9.2 https://github.com/ggml-org/whisper.cpp.git . \
    && cmake -B build -DGGML_NATIVE=OFF -DBUILD_SHARED_LIBS=OFF \
    && cmake --build build --parallel --target whisper-cli \
    && bin="$(find build -type f -name whisper-cli -perm -111 | head -n 1)" \
    && test -n "$bin" \
    && cp "$bin" /usr/local/bin/whisper-cli

FROM golang:1.26.3-bookworm AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        ffmpeg \
        libgomp1 \
        libstdc++6 \
        p7zip-full \
        tesseract-ocr \
        tesseract-ocr-eng \
        tesseract-ocr-rus \
    && rm -rf /var/lib/apt/lists/*
COPY --from=go-build /out/worker /usr/local/bin/worker
COPY --from=go-build /out/api /usr/local/bin/api
COPY --from=whisper-build /usr/local/bin/whisper-cli /usr/local/bin/whisper-cli
ENTRYPOINT ["/usr/local/bin/worker"]
