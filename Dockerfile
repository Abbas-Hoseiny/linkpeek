# syntax=docker/dockerfile:1

FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath -ldflags "-s -w" -o /out/linkpeek ./cmd/linkpeek

FROM alpine:3.20
WORKDIR /app
RUN adduser -D -h /app appuser && mkdir -p /data && chown -R appuser:appuser /app /data
COPY --from=build /out/linkpeek /app/linkpeek
COPY templates/ /app/templates/
COPY static/ /app/static/
EXPOSE 9009
USER appuser
ENV DATA_DIR=/data
ENTRYPOINT ["/app/linkpeek"]
