#!/bin/bash
export PATH="$PATH:/usr/local/go/bin"
export GOPATH=~/goroot

## build without debug information
go build -ldflags "-w -s" -o hpraidmon
