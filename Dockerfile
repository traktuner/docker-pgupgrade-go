# syntax=docker/dockerfile:1

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w" \
    -o /out/docker-pgupgrade .

FROM postgres:19
COPY --from=build /out/docker-pgupgrade /usr/local/bin/docker-pgupgrade
# The base image ships pg_dump, pg_restore, psql, pg_isready and pg_dumpall,
# so the containerized tool can talk to arbitrary host:port endpoints.
ENTRYPOINT ["/usr/local/bin/docker-pgupgrade"]
CMD []
