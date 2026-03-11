FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o housekeeper .

# Minimal runtime image — includes CA certs for TLS connections to ClickHouse/Prometheus
FROM gcr.io/distroless/static-debian12

COPY --from=builder /app/housekeeper /housekeeper

EXPOSE 8080

ENTRYPOINT ["/housekeeper"]
