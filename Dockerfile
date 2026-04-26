# syntax=docker/dockerfile:1.6

# Build stage
FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -buildvcs=false \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/llm-council ./cmd/server

# Runtime stage (distroless, nonroot)
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/llm-council /app/llm-council
USER nonroot:nonroot
EXPOSE 8080
ENV DATA_DIR=/data
VOLUME ["/data"]
ENTRYPOINT ["/app/llm-council"]
