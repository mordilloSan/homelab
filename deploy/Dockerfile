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
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s CMD ["failover-agent", "healthcheck"]
ENTRYPOINT ["failover-agent"]
