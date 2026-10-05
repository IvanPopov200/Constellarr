FROM node:22.23.1-alpine AS frontend
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.27.1-alpine AS backend
RUN apk add --no-cache build-base
WORKDIR /app/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=frontend /app/frontend/dist ./internal/web/dist
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /constellarr ./cmd/constellarr

FROM alpine:3.24 AS runtime
COPY requirements-subtitles.txt /tmp/requirements-subtitles.txt
RUN apk add --no-cache ca-certificates libstdc++ par2cmdline=1.1.1-r0 ffmpeg postgresql18-client python3 py3-numpy py3-pip && \
    apk add --no-cache --virtual .subtitle-build build-base python3-dev && \
    python3 -m venv --system-site-packages /opt/subtitle-tools && \
    /opt/subtitle-tools/bin/pip install --no-cache-dir -r /tmp/requirements-subtitles.txt && \
    apk del .subtitle-build py3-pip && \
    addgroup -g 1000 -S constellarr && adduser -u 1000 -S -G constellarr constellarr
COPY --from=backend /constellarr /usr/local/bin/constellarr
COPY THIRD_PARTY_NOTICES.md /usr/share/constellarr/THIRD_PARTY_NOTICES.md
COPY third_party/licenses /usr/share/constellarr/third_party/licenses
COPY third_party/source-manifest.json third_party/collect_sources.py /usr/share/constellarr/third_party/
USER constellarr
ENV HTTP_ADDR=:8080
ENV PATH="/opt/subtitle-tools/bin:${PATH}"
EXPOSE 8080 51413/tcp 51413/udp 51414/tcp 51414/udp
ENTRYPOINT ["constellarr"]
