# Build stage
FROM docker.io/library/golang:1.24-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the source code
COPY . .

# Build the application
# We use CGO_ENABLED=0 to ensure a statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -o directeur .

# Final stage
FROM docker.io/library/alpine:latest

WORKDIR /app

# Install CA certificates for HTTPS requests (e.g. Gemini API)
RUN apk --no-cache add ca-certificates tzdata

# Create data directory and set environment variable
ENV DIRECTEUR_DATA_DIR=/data
RUN mkdir -p /data

# Copy the binary from the builder stage
COPY --from=builder /app/directeur /usr/local/bin/directeur

# Expose the web server port
EXPOSE 8080

# Run the binary in -serve mode
ENTRYPOINT ["/usr/local/bin/directeur", "-serve"]
