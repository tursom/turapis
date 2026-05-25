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

# Stage 3: minimal runtime (no Python, just compiled binary)
FROM alpine:latest

COPY --from=backend-builder /turapis /usr/local/bin/turapis
COPY --from=frontend-builder /web/dist /static

EXPOSE 8080

ENTRYPOINT ["turapis"]
CMD ["-addr", ":8080", "-db", "/data/turapis.db", "-static-dir", "/static", "-log-file", "/data/turapis.log"]
