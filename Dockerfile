# Multi-stage build for turapis
# Stage 1: build
FROM golang:alpine AS builder

ENV GOTOOLCHAIN=auto

RUN apk add --no-cache git ca-certificates

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /turapis ./cmd/turapis/

# Stage 2: minimal runtime
FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /turapis /usr/local/bin/turapis

EXPOSE 8080

ENTRYPOINT ["turapis"]
CMD ["-addr", ":8080", "-db", "/data/turapis.db"]
