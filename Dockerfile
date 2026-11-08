# The mirror (Everlake's own CDN) as a container: docker build -t everlake-mirror .
# It runs `everlake-launcher mirror`; see docs/cdn-and-peers.md. The Everlake portal's compose file builds it from
# this repository and keeps its store on the server's disk.
FROM golang:1.24-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /everlake-launcher ./cmd/everlake-launcher

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /everlake-launcher /usr/local/bin/everlake-launcher
EXPOSE 8087
ENTRYPOINT ["everlake-launcher"]
CMD ["mirror", "--store", "/store", "--addr", ":8087", "--prefetch", "--quiet"]
