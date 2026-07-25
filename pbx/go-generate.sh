#!/bin/bash
GOPATH_BIN="$(go env GOPATH)/bin"
export PATH="$PATH:$GOPATH_BIN:$GOPATH/bin"

protoc --go_out=../pbx --go_opt=paths=source_relative --go-grpc_out=../pbx --go-grpc_opt=paths=source_relative model.proto
