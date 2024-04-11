#!/bin/bash
# Build binary file and move it to test/bft
if [ -d openmesh-core ]; then
  rm -f openmesh-core
fi
# cd ../.. && GOOS=linux GARCH=amd64 go build -o openmesh-core
cd ../.. && GOOS=linux GARCH=amd64 go build -ldflags "-linkmode external -extldflags -static" -o openmesh-core
mv ./openmesh-core ./Test/testnet/openmesh-core

# Build docker image
cd Test/testnet && docker build -t openmesh-core-testnet:latest .
