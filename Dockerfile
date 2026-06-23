# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Copy dependency files and download
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and build the static Go binary
COPY directeur.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o directeur directeur.go

# Final production stage
FROM alpine:3.19

# Install CA certificates for secure HTTPS API calls to Hammerhead and Google Gemini
RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy the compiled binary and runtime assets
COPY --from=builder /build/directeur .
COPY ride_dashboard.html .
COPY example.fit .
COPY config.json.example config.json

# Expose default HTTP port (Cloud Run will override the port dynamically via the PORT environment variable)
EXPOSE 8080

# Run the server. The port defaults to the PORT env variable if injected by Cloud Run.
ENTRYPOINT ["./directeur", "-serve"]
