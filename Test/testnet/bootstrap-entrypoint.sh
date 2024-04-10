#!/bin/sh
# Copy the default config to the container
cp -r /tmp/cometbft-home /core/default-cometbft-home

# Remove the persistent_peers from config.toml
cd /core/default-cometbft-home/config && sed -i 's/persistent_peers = "538927808bbe633cc5dddfb1fe6fd7b3d62bd434@192.168.20.2:26656"/persistent_peers = ""/' config.toml

# Then the true entrypoint
cd /core && /core/openmesh-core --config /core/conf/config.yml
