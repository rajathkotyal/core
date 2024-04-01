package bft

import (
	"fmt"
	"os"

	cfg "github.com/cometbft/cometbft/config"
	cmtflags "github.com/cometbft/cometbft/libs/cli/flags"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	nm "github.com/cometbft/cometbft/node"
	bftp2p "github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	"github.com/cometbft/cometbft/proxy"
	"github.com/dgraph-io/badger/v3"
	abci "github.com/openmesh-network/core/internal/bft/abci"
	"github.com/openmesh-network/core/collector"
	"github.com/openmesh-network/core/internal/config"
	log "github.com/openmesh-network/core/internal/logger"
	"github.com/spf13/viper"
)

// Instance is the CometBFT instance
type Instance struct {
	Config    *cfg.Config
	BftNode   *nm.Node
	Collector *collector.CollectorInstance
}

// NewInstance initialise a CometBFT instance use the config specified
func NewInstance(db *badger.DB, collector *collector.CollectorInstance) (*Instance, error) {
	conf := cfg.DefaultConfig()
	homeDir := config.Config.BFT.HomeDir
	conf.SetRoot(homeDir)

	log.Info("Loaded config: ", homeDir)

	// Parse CometBFT config
	bftConf := viper.New()
	// TODO XXX: embed this into the executable instead of loading it from filesystem.
	bftConf.SetConfigFile(fmt.Sprintf("%s/%s", homeDir, "config/config.toml"))
	if err := bftConf.ReadInConfig(); err != nil {
		return nil, err
	}
	if err := bftConf.Unmarshal(conf); err != nil {
		return nil, err
	}
	if err := conf.ValidateBasic(); err != nil {
		return nil, err
	}

	pv := privval.LoadFilePV(
		conf.PrivValidatorKeyFile(),
		conf.PrivValidatorStateFile(),
	)
	publicKey, err := pv.GetPubKey()
	if err != nil {
		panic(err)
	}

	app := abci.NewVerificationApp(publicKey.Bytes(), db, collector)

	nodeKey, err := bftp2p.LoadNodeKey(conf.NodeKeyFile())
	if err != nil {
		return nil, err
	}

	log := cmtlog.NewTMLogger(cmtlog.NewSyncWriter(os.Stdout))
	log, err = cmtflags.ParseLogLevel(conf.LogLevel, log, cfg.DefaultLogLevel)
	if err != nil {
		return nil, err
	}

	// Create CometBFT node
	node, err := nm.NewNode(
		conf,
		pv,
		nodeKey,
		proxy.NewLocalClientCreator(app),
		nm.DefaultGenesisDocProviderFunc(conf),
		cfg.DefaultDBProvider,
		nm.DefaultMetricsProvider(conf.Instrumentation),
		log,
	)
	if err != nil {
		return nil, err
	}

	return &Instance{Config: conf, BftNode: node}, nil
}

// Start the CometBFT node
func (i *Instance) Start() {
	go func() {
		if err := i.BftNode.Start(); err != nil {
			log.Fatalf("Failed to start CometBFT node: %s", err.Error())
		}
	}()
}

// Stop the CometBFT node
func (i *Instance) Stop() error {
	err := i.BftNode.Stop()
	i.BftNode.Wait()
	return err
}
