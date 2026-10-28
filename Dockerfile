# syntax=docker/dockerfile:1

FROM nvidia/cuda:12.4.1-devel-ubuntu22.04 AS whisper-build
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential ca-certificates cmake git \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone --depth 1 --branch v1.9.2 https://github.com/ggml-org/whisper.cpp.git . \
    && cmake -B build -DGGML_CUDA=ON -DGGML_NATIVE=OFF -DBUILD_SHARED_LIBS=OFF \
        -DCMAKE_CUDA_ARCHITECTURES="75;80;86;89;90;100;120" \
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

FROM debian:bookworm-slim AS scene
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=go-build /out/worker /usr/local/bin/worker
ENTRYPOINT ["/usr/local/bin/worker"]

FROM nvidia/cuda:12.4.1-runtime-ubuntu22.04 AS transcribe
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates libgomp1 libstdc++6 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=go-build /out/worker /usr/local/bin/worker
COPY --from=whisper-build /usr/local/bin/whisper-cli /usr/local/bin/whisper-cli
ENTRYPOINT ["/usr/local/bin/worker"]

FROM debian:bookworm-slim AS activities
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates ffmpeg p7zip-full \
        tesseract-ocr tesseract-ocr-eng tesseract-ocr-rus \
    && rm -rf /var/lib/apt/lists/*
COPY --from=go-build /out/worker /usr/local/bin/worker
ENTRYPOINT ["/usr/local/bin/worker"]
