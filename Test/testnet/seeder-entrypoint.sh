#!/bin/sh
# Copy the default config to the container
cp -r /tmp/cometbft-home /core/default-cometbft-home

# Give it the same dumb key every time.
echo '{"priv_key":{"type":"tendermint/PrivKeyEd25519","value":"jXw09/JU2OWbjkDH4P9hNFvnfZkpKo3YP5aetwXvYvaPx8dEM3k+7uT2ltfO2Dc95zF/47XPLABg9ZnygtuNoA=="}}' > /core/default-cometbft-home/config/node_key.json
echo '{ "address": "90A7181A5033DB08CB7DB302237B62A7B37E8D46", "pub_key": { "type": "tendermint/PubKeyEd25519", "value": "yhPcJBR2UWg1eo2yDy39usrxxNmGmcE5oLbJIl+v1Qg=" }, "priv_key": { "type": "tendermint/PrivKeyEd25519", "value": "X29pxj5d8QGK/xwNFz1PFoIZyvgMAleIp8XUJ/XboU7KE9wkFHZRaDV6jbIPLf26yvHE2YaZwTmgtskiX6/VCA==" } } ' > /core/default-cometbft-home/config/priv_validator_key.json

# Remove the persistent_peers from config.toml
cd /core/default-cometbft-home/config && \
        sed -i 's/seed_mode = false/seed_mode = true/' config.toml && \
        sed -i 's/seeds = "2112a1fed60fa35e4044e7b8f0aebdc8e6d91f86@192.168.20.251:26656"/seeds = ""/' config.toml && \
        sed -i 's/persistent_peers = ""/persistent_peers = "2a41ce3551fdf4dfdd2100b7ebb02538b7b7e996@192.168.20.250:26656"/' config.toml && \
        sed -i 's/max_num_inbound_peers = 20/max_num_inbound_peers = 1000/' config.toml


# Then the true entrypoint
cd /core && /core/openmesh-core --config /core/conf/config.yml
