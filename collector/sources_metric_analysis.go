package collector

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	bloc "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	log "github.com/openmesh-network/core/internal/logger"
)

type DataWriter struct {
	file   *os.File
	writer *csv.Writer
	mu     sync.Mutex
}

func NewDataWriter(filename string) (*DataWriter, error) {
	file, err := os.Create(filename)
	if err != nil {
		return nil, err
	}

	writer := csv.NewWriter(file)
	return &DataWriter{
		file:   file,
		writer: writer,
	}, nil
}

func (dw *DataWriter) Write(data []string) error {
	dw.mu.Lock()
	defer dw.mu.Unlock()
	if err := dw.writer.Write(data); err != nil {
		return err
	}
	return nil
}

func (dw *DataWriter) Close() error {
	defer dw.file.Close()
	if err := dw.writer.Error(); err != nil {
		return err
	}
	dw.writer.Flush()
	return nil
}

// Handler to subscribe to each source and symbol & measure data size over a
// set period from current time.
func CalculateDataSize(t *testing.T, ctx context.Context, dataWriter *DataWriter, timeToCollect int) {

	// TODO : Implement busiest times of a given src for a given symbol.
	// Fetch data in parts of a whole. 10 mins --> 1 min windows which contain size of
	// msgs received during that period and also number of msgs. Can calculate the load w this.
	// for that time frame.
	startTime := time.Now()
	endTime := startTime.Add(time.Duration(timeToCollect) * time.Second)
	timeFormat := "15:04:05, 02 Jan 2006"
	busyHeader := fmt.Sprintf("Busiest Time (Data collected for %d seconds Btw %s till %s)", timeToCollect, startTime.Format(timeFormat), endTime.Format(timeFormat))

	if err := dataWriter.writer.Write([]string{"Source", "Symbol", "Data Size (bytes)", "Throughput", busyHeader}); err != nil {
		t.Fatalf("Failed to write header to CSV: %s", err)
	}

	var wg sync.WaitGroup
	for _, source := range Sources {
		for _, topic := range source.Topics {
			wg.Add(1)
			go func(src Source, tpc string) {
				defer wg.Done()

				// subscribe and wait till the time period for each src/symbol.
				size := subscribeAndMeasure(ctx, src, tpc, time.NewTimer(time.Duration(timeToCollect)*time.Second))
				throughput := size / int64(timeToCollect)
				fmt.Printf("Data size  for %s - %s: %d bytes, with TP : %d\n", src.Name, tpc, size, throughput)

				record := []string{src.Name, tpc, fmt.Sprintf("%d", size), fmt.Sprintf("%d", throughput)}

				// Write to CSV
				dataWriter.mu.Lock()
				defer dataWriter.mu.Unlock()
				if err := dataWriter.writer.Write(record); err != nil {
					t.Errorf("Failed to write data to CSV for %s - %s: %s", src.Name, tpc, err)
				}
			}(source, topic)
		}
	}
	wg.Wait()

	// Flushing and close writer "after all go routines are done " (imp)
	if err := dataWriter.Close(); err != nil {
		t.Fatal("Failed to flush and close CSV writer:", err)
	}
}

// handles the subscription and updates size the data as its received.
func subscribeAndMeasure(ctx context.Context, source Source, symbol string, timer *time.Timer) int64 {
	msgChan, err := Subscribe(ctx, source, symbol)
	if err != nil {
		log.Info("Error subscribing to source %s with symbol %s: %v", source.Name, symbol, err)
		return 0
	}

	var dataSize int64
	timerLocal := timer

	for {
		select {
		case msg := <-msgChan:

			// TEMP : need to have an identifier for RPC calls,
			// have hardcoded for now.
			if strings.Contains(source.Name, "rpc") {
				// fmt.Println("RPC Token : ", source.Topics, "msg : ", string(msg))

				// Should data from RPC calls be unmarshalled?
				// Not sure what data we need to be storing, have kept the unmarshall
				// function here anyway for future use.
				handleRPCData(msg)
			}
			dataSize += int64(len(msg))

		case <-timerLocal.C:
			fmt.Println("Received entire data for  : ", source.Name, symbol, ".  Now stopping metric collection")
			return dataSize
		case <-ctx.Done():
			return dataSize
		}
	}
}

func handleRPCData(data []byte) bloc.Block {
	var block bloc.Block
	if err := rlp.DecodeBytes(data, &block); err != nil {
		log.Fatalf("RLP Decoding failed: %v", err)
	}
	fmt.Println("block rpc hash data : ", block.Hash())
	return block
}
