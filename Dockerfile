# Build stage
FROM golang:1.26-alpine AS builder

# Install git (needed for go modules) and ca-certificates.
RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a static binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/horde .

# Final stage: minimal image.
FROM alpine:latest

RUN apk --no-cache add ca-certificates
RUN adduser -D -g '' horde

COPY --from=builder /out/horde /usr/local/bin/horde

USER horde
WORKDIR /workspace

ENTRYPOINT ["horde"]
CMD ["serve"]