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

FROM alpine:3.24
RUN apk add --no-cache ca-certificates libstdc++ par2cmdline=1.1.1-r0 && addgroup -g 1000 -S constellarr && adduser -u 1000 -S -G constellarr constellarr
COPY --from=backend /constellarr /usr/local/bin/constellarr
COPY THIRD_PARTY_NOTICES.md /usr/share/constellarr/THIRD_PARTY_NOTICES.md
COPY third_party/licenses /usr/share/constellarr/third_party/licenses
USER constellarr
ENV HTTP_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["constellarr"]
