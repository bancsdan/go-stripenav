# syntax=docker/dockerfile:1
#
# Multi-stage build for the gostripenav binary.
#
# The build context is the repo root because the binary lives in a
# nested module (./cmd) that uses `replace github.com/bancsdan/
# go-stripenav => ..` for local development. We copy both modules and
# build from the cmd module so the replace directive resolves.

FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache library deps first so source edits don't bust the layer.
COPY go.mod go.sum ./
COPY mapping/ ./mapping/
COPY nav/ ./nav/
COPY storeinmem/ ./storeinmem/
COPY *.go ./

# Now the cmd module (replaces back at .. into the library above).
COPY cmd/ ./cmd/

WORKDIR /src/cmd
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux \
    go build \
      -trimpath \
      -ldflags='-s -w -extldflags "-static"' \
      -o /out/gostripenav ./gostripenav

# Final image: distroless static, ~2 MB beyond the binary.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gostripenav /usr/local/bin/gostripenav

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/gostripenav"]
