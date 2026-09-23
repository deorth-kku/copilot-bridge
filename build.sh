#/bin/bash
mkdir -p build
go build -trimpath -ldflags "-s -w -buildid= " -o build/copilot-bridge .