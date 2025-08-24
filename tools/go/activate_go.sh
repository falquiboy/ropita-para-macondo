#!/usr/bin/env bash
THIS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export GOROOT="$THIS_DIR/go"
export GOPATH="$THIS_DIR/gopath"
export PATH="$GOROOT/bin:$GOPATH/bin:$PATH"
export GO111MODULE=on
hash -r
which go || true
go version || true
