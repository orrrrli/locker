# Build context: api/
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# Distroless static: CA certs and tzdata included, no shell, runs as uid 65532.
# For an emergency shell, swap the tag to :debug-nonroot (busybox) and redeploy.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /api
ENTRYPOINT ["/api"]
