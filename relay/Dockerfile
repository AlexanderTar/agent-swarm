# Builds the Swarm relay from the repo root: docker build -f relay/Dockerfile .
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/swarm-relay ./cmd/swarm-relay
COPY internal/relay ./internal/relay
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /swarm-relay ./cmd/swarm-relay

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /swarm-relay /swarm-relay
# Railway injects PORT; RELAY_SECRET (64+ hex chars) is a service variable.
EXPOSE 8080
ENTRYPOINT ["/swarm-relay"]
