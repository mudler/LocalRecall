# Build the Go binary in a separate stage utilizing Makefile
FROM golang:1.24 AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN make build

# Use the official Ubuntu 22.04 image as a base for the final image
FROM scratch AS base

# Install ca-certificates to ensure TLS verification works
#RUN apt-get update && apt-get install -y ca-certificates && update-ca-certificates

COPY --from=builder /app/localrecall /localrecall
COPY --from=builder /etc/ssl /etc/ssl
COPY --from=builder /tmp /tmp

# Expose necessary ports
EXPOSE 8080

# Set default command to start the Go application
ENTRYPOINT [ "/localrecall" ]
