FROM golang:1.25 AS builder
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /csi-volume-device-exporter ./cmd/exporter

FROM gcr.io/distroless/static-debian12
COPY --from=builder /csi-volume-device-exporter /csi-volume-device-exporter
ENTRYPOINT ["/csi-volume-device-exporter"]
