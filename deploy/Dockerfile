FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY internal/ internal/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /failover-agent .

# docker:cli already has the docker CLI and the compose plugin.
FROM docker:cli
RUN apk add --no-cache btrfs-progs iputils-arping iputils-ping tzdata ca-certificates
COPY --from=build /failover-agent /usr/local/bin/failover-agent
# Dozzle's icon for the container: the page's favicon (the header logo).
LABEL dev.dozzle.icon="data:image/svg+xml;base64,PHN2ZyB4bWxucz0naHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmcnIHZpZXdCb3g9JzAgMCAyNCAyNCc+PHJlY3Qgd2lkdGg9JzI0JyBoZWlnaHQ9JzI0JyByeD0nNicgZmlsbD0nIzIwNTJjMicvPjxwYXRoIGZpbGw9JyNmZmYnIHRyYW5zZm9ybT0ndHJhbnNsYXRlKDIuNCAyLjQpIHNjYWxlKC44KScgZD0nbTIxIDlsLTQtNHYzaC03djJoN3YzTTcgMTFsLTQgNGw0IDR2LTNoN3YtMkg3eicvPjwvc3ZnPg=="
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s CMD ["failover-agent", "healthcheck"]
ENTRYPOINT ["failover-agent"]
