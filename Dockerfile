# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.build=${VERSION}" -o /out/hidden-isle ./cmd/hidden-isle \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hidden-isle /usr/local/bin/hidden-isle
# A named volume at /data inherits this ownership (nonroot = 65532).
COPY --from=build --chown=65532:65532 /out/data /data
ENV HI_DATA_DIR=/data
VOLUME /data
EXPOSE 8080 8081
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/usr/local/bin/hidden-isle", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/hidden-isle"]
