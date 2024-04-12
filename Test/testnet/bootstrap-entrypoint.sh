#!/bin/sh
# Copy the default config to the container
cp -r /tmp/cometbft-home /core/default-cometbft-home

#  sed -i 's/persistent_peers = "2a41ce3551fdf4dfdd2100b7ebb02538b7b7e996@192.168.20.30:26656"/persistent_peers = "ca0dc3d9b7e168fd3135c1da56b4abc2e55bd04e@139.180.178.149:26656"/' config.toml && \

# Remove the persistent_peers from config.toml
cd /core/default-cometbft-home/config && \
  sed -i 's/persistent_peers = "2a41ce3551fdf4dfdd2100b7ebb02538b7b7e996@192.168.20.250:26656"/persistent_peers = ""/' config.toml && \
  sed -i 's/prometheus = false/prometheus = true/' config.toml

# Then the true entrypoint
cd /core && /core/openmesh-core --config /core/conf/config.yml
