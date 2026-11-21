# The builder runs on the build host's platform and cross-compiles for the target, so arm64 images build without emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o housekeeper .

# Minimal runtime image — includes CA certs for TLS connections to ClickHouse/Prometheus
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /app/housekeeper /housekeeper

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/housekeeper"]
