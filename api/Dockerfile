# Build context: api/
# mirror.gcr.io: anonymous ECR Public pulls hit a per-IP data limit on shared CI runners (OPS-CI-1).
FROM mirror.gcr.io/library/golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# Distroless static: CA certs and tzdata included, no shell, runs as uid 65532.
# For an emergency shell, swap the tag to :debug-nonroot (busybox) and redeploy.
FROM gcr.io/distroless/static-debian12:nonroot
# Soft heap limit below the 256M container limit, so the GC works harder before
# the kernel OOM-kills the process. docker-compose.yml sets the same value.
ENV GOMEMLIMIT=200MiB
COPY --from=build /out/api /api
ENTRYPOINT ["/api"]
