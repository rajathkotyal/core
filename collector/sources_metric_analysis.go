package collector

import (
	"context"
	"encoding/binary"
	"encoding/csv"

	jsoniter "github.com/json-iterator/go"

	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buger/jsonparser"
	"github.com/klauspost/compress/zstd"
	"github.com/openmesh-network/core/internal/config"
	log "github.com/openmesh-network/core/internal/logger"
	"github.com/pierrec/lz4"
	"go.mongodb.org/mongo-driver/bson"
)

// using this for now since we are unmarshaling every value
var jsoniterator = jsoniter.ConfigCompatibleWithStandardLibrary

type DataWindow struct {
	SourceName   string
	Symbol       string
	StartTime    string
	DataSize     int64
	MessageCount int
	Throughput   float64
}

type DataCollector struct {
	Source        Source
	TimeWindow    time.Duration
	DataWindows   map[string]*DataWindow
	BusiestWindow *DataWindow
}

type SourceMetric struct {
	sourceName                     string
	sourceTotalThroughputPerWindow map[string]float64
	busiestTimeWindowStartTime     string
	busiestThroughput              float64
	mu                             sync.Mutex
}

func NewSourceMetric(sourceName string) *SourceMetric {
	return &SourceMetric{
		sourceName:                     sourceName,
		sourceTotalThroughputPerWindow: make(map[string]float64),
		busiestTimeWindowStartTime:     "",
		busiestThroughput:              -1,
	}
}

func NewDataCollector(source Source, windowTimeSize time.Duration) *DataCollector {
	return &DataCollector{
		Source:        source,
		TimeWindow:    windowTimeSize,
		DataWindows:   make(map[string]*DataWindow),
		BusiestWindow: &DataWindow{Throughput: -1},
	}
}

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

// Handler to subscribe to each source and symbol & measure data size over a set period from current time.
func CalculateDataSize(t *testing.T, ctx context.Context, dataWriter *DataWriter, timeToCollect int, timeFrameWindowSize int) {

	config.Path = "../../core"
	config.Name = "config"
	config.ParseConfig(config.Path, true)
	log.InitLogger()

	// Busiest times of a given src for a given symbol --> To detect high trading volume for a given symbol.
	// Fetch data in parts of a whole. 10 mins --> 1 min windows which contain size of
	// msgs received during that period and also number of msgs. Can calculate the load w this. --> Throughput = size / window time frame.
	// for that time frame.
	startTime := time.Now()
	endTime := startTime.Add(time.Duration(timeToCollect) * time.Second)
	timeFrame := time.Duration(timeFrameWindowSize) * time.Second
	timeFormat := "15:04:05, 02 Jan 2006"
	busyHeader := fmt.Sprintf("Busiest Time (Data collected for %d seconds Btw %s till %s)", timeToCollect, startTime.Format(timeFormat), endTime.Format(timeFormat))

	//headerCSv
	if err := dataWriter.writer.Write([]string{"Source", "Symbol", "Data Size (bytes)", "Throughput", busyHeader, "Throughput in the interval", "messages count received during interval"}); err != nil {
		t.Fatalf("Failed to write header to CSV: %s", err)
	}

	var wg sync.WaitGroup
	sourceMetricsList := make([]*SourceMetric, 0)

	for _, source := range Sources {
		sourceMetric := NewSourceMetric(source.Name)
		sourceMetricsList = append(sourceMetricsList, sourceMetric)
		for _, topic := range source.Topics {
			dc := NewDataCollector(source, timeFrame)
			wg.Add(1)
			go func(src Source, tpc string, srcMetric *SourceMetric) {
				defer wg.Done()

				// subscribe and wait till the time period for each src/symbol.
				size, busiestWindow, _ := subscribeAndMeasure(ctx, src, tpc, time.NewTimer(time.Duration(timeToCollect)*time.Second), dc, timeFrameWindowSize, srcMetric)
				throughput := size / int64(timeToCollect)
				// log.Infof("Data size  for %s - %s: %d bytes, with TP : %d \n", src.Name, tpc, size, throughput)
				throughputStr := fmt.Sprintf("%.2f", busiestWindow.Throughput)

				// Compression benchmark
				if strings.Contains(src.Name, "coinbase") {

					fmt.Println("Compressing : ")
					compressBSONFile("compressed_files/"+"test_bson_coinbase.bson", "compressed_files/"+"test_compressed_bson_coinbase.zst", 4094)

					compressBSONFileLZ4("compressed_files/"+"test_bson_coinbase.bson", "compressed_files/"+"test_compressed_bson_coinbase.lz4", 4094)

					fmt.Println("Decompressing : ")
					err := decompressAndReadBSON("compressed_files/test_compressed_bson_coinbase.zst")
					fmt.Println("decomp err ", err)

					err1 := decompressAndReadBSONLZ4("compressed_files/test_compressed_bson_coinbase.lz4")
					fmt.Println("decomp lz4 err ", err1)
				}

				record := []string{src.Name, tpc, fmt.Sprintf("%d", size), fmt.Sprintf("%d", throughput), busiestWindow.StartTime, throughputStr, fmt.Sprintf("%d", busiestWindow.MessageCount)}

				dataWriter.mu.Lock()
				defer dataWriter.mu.Unlock()
				if err := dataWriter.writer.Write(record); err != nil {
					t.Errorf("Failed to write data to CSV for %s - %s: %s", src.Name, tpc, err)
				}
			}(source, topic, sourceMetric)
		}
	}
	wg.Wait()

	// Get the busiest source at a given time period
	for _, sourceMetric := range sourceMetricsList {
		log.Infof("Source: %s, Max Throughput: %.2f during %s \n",
			sourceMetric.sourceName,
			sourceMetric.busiestThroughput,
			sourceMetric.busiestTimeWindowStartTime)
	}

	// Flushing and close writer "after all go routines are done " (imp)
	if err := dataWriter.Close(); err != nil {
		t.Fatal("Failed to flush and close CSV writer:", err)
	}
}

// handles the subscription and updates size the data as its received.
func subscribeAndMeasure(ctx context.Context, source Source, symbol string, timer *time.Timer, dc *DataCollector, timeFrameWindowSize int, sourceMetrics *SourceMetric) (int64, *DataWindow, string) {
	msgChan, err := Subscribe(ctx, source, symbol)
	if err != nil {
		log.Infof("Error subscribing to source %s with symbol %s: %v", source.Name, symbol, err)
		return 0, &DataWindow{}, " "
	}

	var dataSize int64
	var messageCount int64
	timerLocal := timer

	// InitialThroughput := float64(len(msgChan)) / float64(1)
	globalWindow := &DataWindow{DataSize: int64(len(msgChan)), MessageCount: 1, Throughput: -1}
	windowChange := make(chan string, 1)
	oldWindowKey := ""

	var file *os.File
	var filePath string
	if strings.Contains(source.Name, "coinbase") {
		filePath := fmt.Sprintf("test_bson_%s", sourceMetrics.sourceName+".bson")

		// Append, Create if not exists, Write only mode.
		file, err = os.OpenFile("compressed_files/"+filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
		if err != nil {
			log.Fatalf("Failed to open file: %v", err)
			panic(err)
		}
	}

	// Manage window changes
	go func() {
		for {
			select {
			case windowKey := <-windowChange:
				window := dc.DataWindows[windowKey]
				if window != nil {
					throughput := float64(window.DataSize) / float64(timeFrameWindowSize)
					window.Throughput = throughput

					if throughput > globalWindow.Throughput {
						globalWindow = window
					}
					// log.Infoln("Window changed for ", window.SourceName, window.Symbol, " . Startime : ", window.StartTime, " Window Throughput :", throughput, " Current max throughput : ", globalWindow.Throughput, " Message count in window : ", window.MessageCount)
				}
			case <-ctx.Done():
				close(windowChange)
				return
			}
		}
	}()

	for {
		select {
		case msg := <-msgChan:

			if int64(len(msg)) == 0 || msg == nil {
				continue
			}

			var bsonData []byte

			if !strings.Contains(source.Name, "rpc") && !strings.Contains(source.Name, "binance") && !strings.Contains(source.Name, "rpc") {

				var coinbase interface{}

				// using encoding json as of now
				if err := jsoniterator.Unmarshal(msg, &coinbase); err != nil {
					log.Fatalf("Failed to unmarshal JSON: %v", err)
					continue
				}

				bsonData, err = bson.Marshal(coinbase) // of type []byte
				if err != nil {
					log.Fatalf("Failed to marshal to BSON: %v", err)
					continue
				}

				bsonLength := len(bsonData)

				// fmt.Println("BSON data : ", source.Name, bsonData, " length : ", bsonLength)

				// this buffer that will hold length and data
				buffer := make([]byte, 4+bsonLength)

				// Write length of the BSON data to buffer (big-endian format)
				// for larger data --> use :
				// binary.BigEndian.PutUint64(buffer[0:4], uint64(bsonLength))
				binary.BigEndian.PutUint32(buffer[0:4], uint32(bsonLength))

				// Copies BSON data to buffer
				copy(buffer[4:], bsonData)

				_, err = file.Write(buffer)
				if err != nil {
					log.Fatalf(source.Name, " Failed to write to file: %v", err)
				}
			}

			currentTime := time.Now()

			// this rounds down the current time to the timeWindow
			// if say window is 10 min, 21:16:57 is rounded to 21:10:00
			// such that we can have a map of all elements between 21:10:00 till 21:20:00
			// Format preserves quality by conv to string.
			windowKey := currentTime.Truncate(dc.TimeWindow).Format(time.RFC3339)

			window, exists := dc.DataWindows[windowKey]
			if !exists {
				if oldWindowKey == "" {
					windowChange <- windowKey
				} else {
					windowChange <- oldWindowKey
				}
				window = &DataWindow{
					SourceName:   source.Name,
					Symbol:       symbol,
					StartTime:    windowKey,
					DataSize:     0,
					MessageCount: 0,
				}
				dc.DataWindows[windowKey] = window
			}
			oldWindowKey = windowKey
			dc.DataWindows[windowKey].DataSize += int64(len(msg))
			dc.DataWindows[windowKey].MessageCount += 1

			dataSize += int64(len(msg))
			messageCount += 1

			// Aggregate source throughput per window
			// --> Every roiutine (source + symbol) has global structure sourceMetrics
			// they lock and change the throughputs as and when windows change
			// --> This was, cross routine access is localized to that source's topics
			// --> Easier to extract data as well later on.
			sourceMetrics.mu.Lock()
			currentThroughput := float64(window.DataSize) / float64(timeFrameWindowSize)
			totalThroughput := sourceMetrics.sourceTotalThroughputPerWindow[windowKey]
			sourceMetrics.sourceTotalThroughputPerWindow[windowKey] = totalThroughput + currentThroughput
			if totalThroughput+currentThroughput > sourceMetrics.busiestThroughput {
				sourceMetrics.busiestThroughput = totalThroughput + currentThroughput
				sourceMetrics.busiestTimeWindowStartTime = windowKey
			}
			sourceMetrics.mu.Unlock()

			// Debug
			// fmt.Println("Message received from ", source, symbol, " : ", string(msg))

		case <-timerLocal.C:
			// log.Infoln("Received entire data for  : ", source.Name, symbol, ".  Now stopping metric collection, message ct : ", messageCount)

			file.Close()
			fmt.Println("Closed file ", filePath)
			time.Sleep(3 * time.Second)
			return dataSize, globalWindow, filePath
		case <-ctx.Done():
			return dataSize, globalWindow, filePath
		}
	}
}

// If using json parser, use this func to dettermine datatypes etc.
func handleKeyValueType(key, value []byte, dataType jsonparser.ValueType, offset int) error {
	fmt.Printf("Key: %s, Value: %s, Type: %v\n", string(key), string(value), dataType)
	return nil
}

func compressBSONFileOld(sourceFilePath, destFilePath string) error {

	sourceFile, err := os.Open(sourceFilePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.OpenFile(destFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatalf("Failed to open file: %v", err)
		panic(err)
	}
	defer destFile.Close()

	// default compression level
	encoder, err := zstd.NewWriter(destFile)
	if err != nil {
		return err
	}
	defer encoder.Close()

	_, err = io.Copy(encoder, sourceFile)
	return err
}

func decompressAndReadBSON(destFilePath string) error {

	compressedFile, err := os.Open(destFilePath)
	if err != nil {
		return fmt.Errorf("failed to open compressed file: %v", err)
	}
	defer compressedFile.Close()

	decoder, err := zstd.NewReader(compressedFile)
	if err != nil {
		return fmt.Errorf("failed to create zstd reader: %v", err)
	}
	defer decoder.Close()

	// length prefix buffer, to read only length and skip to the next doc
	bsonBuffer := make([]byte, 4)
	msgCount := 0

	for {
		// Reads length of the next BSON document
		if _, err := io.ReadFull(decoder, bsonBuffer); err != nil {
			if err == io.EOF {
				fmt.Println("Reached end of file")
				break
			}
			return fmt.Errorf("failed to read BSON length: %v", err)
		}

		length := int(binary.BigEndian.Uint32(bsonBuffer))
		if length <= 0 {
			return fmt.Errorf("invalid BSON document length: %d", length)
		}

		bsonData := make([]byte, length)
		// Read actual BSON data
		if _, err := io.ReadFull(decoder, bsonData); err != nil {
			return fmt.Errorf("failed to read BSON data: %v", err)
		}

		var msg map[string]interface{}

		// Unmarshal BSON to generic interface
		if err := bson.Unmarshal(bsonData, &msg); err != nil {
			return fmt.Errorf("failed to unmarshal BSON: %v", err)
		}

		_, err := jsoniterator.MarshalIndent(msg, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %v", err)
		}

		msgCount++
		fmt.Println("Decompressed JSON count :", msg, msgCount)
	}

	return nil
}

func compressBSONFileLZ4Old(sourceFilePath, destFilePath string) error {

	sourceFile, err := os.Open(sourceFilePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.OpenFile(destFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatalf("Failed to open file: %v", err)
		panic(err)
	}
	defer destFile.Close()

	encoder := lz4.NewWriter(destFile)
	defer encoder.Close()

	_, err = io.Copy(encoder, sourceFile)
	return err
}

func decompressAndReadBSONLZ4(sourceFilePath string) error {

	compressedFile, err := os.Open(sourceFilePath)
	if err != nil {
		return fmt.Errorf("failed to open compressed file: %v", err)
	}
	defer compressedFile.Close()

	decoder := lz4.NewReader(compressedFile)

	bsonBuffer := make([]byte, 4)
	msgCount := 0

	for {
		if _, err := io.ReadFull(decoder, bsonBuffer); err != nil {
			if err == io.EOF {
				fmt.Println("Reached end of file")
				break
			}
			return fmt.Errorf("failed to read BSON length: %v", err)
		}

		length := int(binary.BigEndian.Uint32(bsonBuffer))
		if length <= 0 {
			return fmt.Errorf("invalid BSON document length: %d", length)
		}

		bsonData := make([]byte, length)
		if _, err := io.ReadFull(decoder, bsonData); err != nil {
			return fmt.Errorf("failed to read BSON data: %v", err)
		}

		var msg map[string]interface{}
		if err := bson.Unmarshal(bsonData, &msg); err != nil {
			return fmt.Errorf("failed to unmarshal BSON: %v", err)
		}

		msgCount++
		fmt.Println("Lz4 Decompressed BSON document count:", msg, msgCount)

	}

	return nil
}

func compressBSONFile(sourceFilePath, destFilePath string, chunkSize int) error {
	sourceFile, err := os.Open(sourceFilePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(destFilePath)
	if err != nil {
		return err
	}
	defer destFile.Close()

	encoder, err := zstd.NewWriter(destFile)
	if err != nil {
		return err
	}
	defer encoder.Close()

	buf := make([]byte, chunkSize)
	for {
		n, err := sourceFile.Read(buf)
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if n > 0 {
			if _, err := encoder.Write(buf[:n]); err != nil {
				return err
			}
		}
	}
	return nil
}

func compressBSONFileLZ4(sourceFilePath, destFilePath string, chunkSize int) error {
	sourceFile, err := os.Open(sourceFilePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(destFilePath)
	if err != nil {
		return err
	}
	defer destFile.Close()

	encoder := lz4.NewWriter(destFile)
	defer encoder.Close()

	buf := make([]byte, chunkSize)
	for {
		n, err := sourceFile.Read(buf)
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if n > 0 {
			if _, err := encoder.Write(buf[:n]); err != nil {
				return err
			}
		}
	}
	return nil
}
