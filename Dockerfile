# Multi-stage build for turapis
# Stage 1: frontend build
FROM node:alpine AS frontend-builder
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: backend build
FROM golang:alpine AS backend-builder

ENV GOTOOLCHAIN=auto

RUN apk add --no-cache git ca-certificates

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /turapis ./cmd/turapis/

# Stage 3: PyInstaller build for codex_oauth_browser
FROM alpine:latest AS pyinstaller-builder

RUN apk add --no-cache python3 py3-pip chromium chromium-chromedriver binutils

RUN pip3 install --no-cache-dir --break-system-packages \
    undetected-chromedriver selenium pyinstaller

COPY scripts/codex_oauth_browser.py /build/codex_oauth_browser.py

RUN pyinstaller --onefile --name codex_oauth_browser \
    /build/codex_oauth_browser.py

# Stage 4: minimal runtime (no Python, just chromium + compiled binary)
FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata chromium chromium-chromedriver \
    && rm -rf /var/cache/apk/*

COPY --from=backend-builder /turapis /usr/local/bin/turapis
COPY --from=frontend-builder /web/dist /static
COPY --from=pyinstaller-builder /dist/codex_oauth_browser /usr/local/bin/codex_oauth_browser

EXPOSE 8080

ENTRYPOINT ["turapis"]
CMD ["-addr", ":8080", "-db", "/data/turapis.db", "-static-dir", "/static", "-log-file", "/data/turapis.log"]
