package bft

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strconv"
	"time"

	cfg "github.com/cometbft/cometbft/config"
	cmtflags "github.com/cometbft/cometbft/libs/cli/flags"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	nm "github.com/cometbft/cometbft/node"
	bftp2p "github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/types"
	"google.golang.org/protobuf/proto"

	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	"github.com/dgraph-io/badger/v3"
	"github.com/openmesh-network/core/collector"
	abci "github.com/openmesh-network/core/internal/bft/abci"
	otypes "github.com/openmesh-network/core/internal/bft/types"
	"github.com/openmesh-network/core/internal/config"
	log "github.com/openmesh-network/core/internal/logger"
	"github.com/spf13/viper"
)

// Instance is the CometBFT instance
type Instance struct {
	Config    *cfg.Config
	BftNode   *nm.Node
	Collector *collector.CollectorInstance
	app       *abci.VerificationApp
	collector *collector.CollectorInstance
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

	app := abci.NewVerificationApp(publicKey.Bytes(), db)

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

	// events := node.EventBus()
	// data := types.EventDataTx{}

	if err != nil {
		return nil, err
	}

	return &Instance{Config: conf, BftNode: node, app: app, collector: collector}, nil
}

// Start the CometBFT node
func (inst *Instance) Start(ctx context.Context) {
	eventBus := inst.BftNode.EventBus()

	newBlock, err := eventBus.Subscribe(ctx, "mainId", types.EventQueryNewBlock)
	if err != nil {
		panic(err)
	}

	// Event handler
	go func() {
		for {
			select {
			case <-ctx.Done():
				eventBus.UnsubscribeAll(ctx, "mainId")
				return
			case <-newBlock.Canceled():
				eventBus.UnsubscribeAll(ctx, "mainId")
				return
			case <-newBlock.Out():
				requests := inst.app.GetRequestsDue()

				var summaries []collector.Summary
				if requests != nil {
					summaries = inst.collector.SubmitRequests(requests)
				} else {
					log.Debug("No requests this block :(")
				}

				for i := range summaries {
					log.Debug("Went through: ", i, " summaries. ", summaries[i])
					s := summaries[i]
					r := requests[i]

					if len(s.DataHashes) < 1 {
						log.Debug("No hash for this source :(")
					} else {
						// Format as a transactionMessage
						transactionMessage := otypes.Transaction{
							Owner:     "John Doe",
							Signature: strconv.Itoa(i),
							Type:      *otypes.TransactionType_VerificationTransaction.Enum(),
						}

						transactionMessage.Data = &otypes.Transaction_VerificationData{
							VerificationData: &otypes.VerificationTransactionData{
								// XXX: Actually provide attestation here.
								Attestation: "",
								// XXX: Need to decide how we're building the cids.
								// There's a tradeoff between blockchain size and download speed.
								Cid:        s.DataHashes[0].String(),
								Datasource: r.Source.Name + "-" + r.Source.Topics[r.Topic],
								// XXX: Should this be the time it started being recorded or ended?
								Timestamp: time.Now().Unix(),
							},
						}

						transactionBytes, err := proto.Marshal(&transactionMessage)
						if err != nil {
							panic(err)
						}

						transaction := types.Tx(transactionBytes[:])

						env, err := inst.BftNode.ConfigureRPC()

						if err != nil {
							panic(err)
						}

						log.Debug("Pushing transaction with hash: ", sha256.Sum256(transactionBytes))
						_, err = env.BroadcastTxAsync(&rpctypes.Context{}, transaction)

						if err != nil {
							panic(err)
						}
						log.Debug("Succesfully pushed transaction!")

						log.Debug("Got summary: ", s)
					}
				}
			}
		}
	}()

	go func() {
		// This blocks:
		if err := inst.BftNode.Start(); err != nil {
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
