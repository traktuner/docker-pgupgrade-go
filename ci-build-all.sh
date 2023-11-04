#!/bin/sh
GOOS=linux GOARCH=amd64 ./build.sh -o docker-pgupgrade-linux-amd64
GOOS=linux GOARCH=arm64 ./build.sh -o docker-pgupgrade-linux-arm64
GOOS=darwin GOARCH=amd64 ./build.sh -o docker-pgupgrade-macos-amd64
GOOS=darwin GOARCH=arm64 ./build.sh -o docker-pgupgrade-macos-arm64
