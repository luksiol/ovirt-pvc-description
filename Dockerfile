FROM golang:1.22 AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/

ARG VERSION=unknown

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -mod=readonly -trimpath \
    -ldflags="-s -w" \
    -o /controller ./cmd/controller

FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION=unknown

LABEL org.opencontainers.image.title="ovirt-pvc-controller"
LABEL org.opencontainers.image.version="${VERSION}"

COPY --from=builder /controller /controller

USER 65532:65532

ENTRYPOINT ["/controller"]
