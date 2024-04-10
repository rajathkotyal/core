#!/bin/sh
# Initialise new configuration for this container
cometbft init --home /core/default-cometbft-home
echo "Successfully initialise default CometBFT home in /core/default-cometbft-home"

# Overwrite shared configurations
cp /tmp/cometbft-home/config/genesis.json /core/default-cometbft-home/config/genesis.json
cp /tmp/cometbft-home/config/config.toml /core/default-cometbft-home/config/config.toml
echo "Successfully overwrite shared settings"

# Finally, the true entrypoint
/core/openmesh-core --config /core/conf/config.yml
