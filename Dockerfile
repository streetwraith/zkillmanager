# syntax=docker/dockerfile:1
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /zkillmanager ./cmd/zkillmanager

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /zkillmanager /zkillmanager
EXPOSE 8080
USER nonroot:nonroot
# The binary probes its own /healthz (no shell/curl in distroless). At least one flag is
# required for Coolify's parseHealthcheckFromDockerfile to detect the healthcheck.
HEALTHCHECK --interval=30s --timeout=3s --start-period=30s --retries=3 \
  CMD ["/zkillmanager", "-healthcheck"]
ENTRYPOINT ["/zkillmanager"]
