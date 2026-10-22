# Compile on the build host's platform and cross-compile for the target, so
# multi-arch builds don't run the Go toolchain under emulation.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /relay ./cmd/relay

FROM gcr.io/distroless/static-debian12
COPY --from=build /relay /relay
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/relay"]
