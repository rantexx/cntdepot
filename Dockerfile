# Cloud image of depotd: the web app is built first and embedded in the Go binary.

FROM node:24-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN mkdir -p ../internal/webui/dist && npm run build

FROM golang:1.27-bookworm AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /depotd ./cmd/depotd

# Runs as root only because photos go to a mounted volume (/data) until they move
# to object storage; switch to the :nonroot image then.
FROM gcr.io/distroless/static-debian12
COPY --from=go /depotd /depotd
EXPOSE 8080
ENTRYPOINT ["/depotd"]
CMD ["api", "-env", "production", "-addr", ":8080", "-blobs", "/data/blobs"]
