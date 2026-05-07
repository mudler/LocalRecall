# Build the Go binary in a separate stage utilizing Makefile.
# Aligned with LocalAGI / LocalAI (both on Go 1.26). LocalRecall's go.mod
# directive is 1.25.0; the previous golang:1.24 base started failing
# `go mod download` after the go-pdfium swap brought in newer x/* deps.
FROM golang:1.26 AS builder

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
