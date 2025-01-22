# ---------------------------------------------------------------------------
# Stage 1: Build the Go binary in a lightweight build environment
# ---------------------------------------------------------------------------
FROM golang:1.24-rc-alpine AS builder

# Create and switch to a working directory
WORKDIR /app

# Copy go.mod and go.sum first (to leverage Docker layer caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of your source
COPY . .

# Build the main executable
RUN CGO_ENABLED=0 GOOS=linux go build -o ninetyfive ./cmd/ninetyfive/main.go


# ---------------------------------------------------------------------------
# Stage 2: Use a minimal base image (distroless) to run the binary
# ---------------------------------------------------------------------------
FROM gcr.io/distroless/base-debian11

# Copy the binary from the builder stage
COPY --from=builder /app/ninetyfive /bin/ninetyfive

# (Optional) If you need static config files, place them in the same path as in your code
# COPY --from=builder /app/configs /app/configs

# Expose port 8080 for Cloud Run
EXPOSE 8080

# Run the compiled binary
ENTRYPOINT ["/bin/ninetyfive"]