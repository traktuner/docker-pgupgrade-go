#!/bin/sh
GO111MODULE=off GOOS=linux GOARCH=amd64 ./build.sh -o docker-pgupgrade-linux-amd64
GO111MODULE=off GOOS=linux GOARCH=arm64 ./build.sh -o docker-pgupgrade-linux-arm64
GO111MODULE=off GOOS=darwin GOARCH=amd64 ./build.sh -o docker-pgupgrade-macos-amd64
GO111MODULE=off GOOS=darwin GOARCH=arm64 ./build.sh -o docker-pgupgrade-macos-arm64