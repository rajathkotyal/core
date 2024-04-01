package collector

import (
	"context"

	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multicodec"
	log "github.com/openmesh-network/core/internal/logger"
	"github.com/sourcegraph/conc"
)

type Request struct {
	Source Source
	Topic  int
}

type Summary struct {
	Request Request
	// XXX: This might not be efficient, array of pointers means many cache misses.
	// Not sure if the Go compiler will realize we want these sequentially in memory.
	// But whatever man we're doing like 1-5 of these a second.
	DataHashes []cid.Cid
}

type CollectorWorker struct {
	summary *Summary
	request Request
	message chan []byte

	// Could make these into the same function.
	pause  chan bool
	resume chan bool
}

type CollectorInstance struct {
	ctx                       context.Context
	workers                   [CONNECTIONS_MAX]CollectorWorker
	workerWaitGroup           conc.WaitGroup
	requestsByPriorityCurrent []Request
	requestsByPriorityNew     []Request
	summariesNew              [CONNECTIONS_MAX]Summary
	summariesOld              [CONNECTIONS_MAX]Summary
	subscriptionsContext      context.Context
	subscriptionsCancel       context.CancelFunc
}

const CONNECTIONS_MAX = 1

// const BUFFER_SIZE_MAX = 1024
// const BUFFER_MAX = 1024

func New() *CollectorInstance {
	return &CollectorInstance{}
}

func (ci *CollectorInstance) SubmitRequests(requestsSortedByPriority []Request) []Summary {
	// XXX: Wasteful, shouldn't have to remake but whatever.
	ci.requestsByPriorityNew = make([]Request, len(requestsSortedByPriority))

	copy(ci.requestsByPriorityNew, requestsSortedByPriority)

	if ci.subscriptionsCancel != nil {
		ci.subscriptionsCancel()
	}

	ci.subscriptionsContext, ci.subscriptionsCancel = context.WithCancel(ci.ctx)

	log.Info("Pausing workers")
	for i := range ci.workers {
		log.Info("Pausing worker ", i)
		// This tells worker to flush the messages buffer and stop reading the summary pointer.
		ci.workers[i].pause <- true
	}

	// Now the old summaries are up to date.
	copy(ci.summariesOld[:], ci.summariesNew[:])

	log.Info("Subscribing to requests.")
	for i := 0; i < min(len(ci.workers), len(requestsSortedByPriority)); i++ {
		r := requestsSortedByPriority[i]

		// Subscribe to new source.
		// TODO: If a worker is already subscribed to a source don't end the subscription.
		// Significant rewrite, but might improve performance.
		log.Info("Subscribing ", i)
		messageChannel, err := Subscribe(ci.subscriptionsContext, r.Source, r.Source.Topics[r.Topic])
		if err != nil {
			// XXX: Handle this case by skipping this request.
			// Panicking now to highlight this.
			panic(err)
		}

		ci.workers[i].summary.Request = r
		ci.workers[i].message = messageChannel
	}

	for i := range ci.workers {
		// Now unpause.
		log.Info("Resuming ", i)
		ci.workers[i].resume <- true
	}

	maxSummaries := min(len(ci.summariesOld), len(requestsSortedByPriority))
	return ci.summariesOld[:maxSummaries]
}

func (cw *CollectorWorker) run(ctx context.Context, buffer []byte) {
	log.Info("Started worker.")

	if buffer == nil {
		panic("Buffer is nil dummy.")
	}
	if len(buffer) < 100 {
		panic("Buffer is too small, is this an error?")
	}

	// XXX: Maybe implement this function in RP? Also it will crash if length == 0
	// Also I could move this to another function.
	summaryAppend := func(summary *Summary, buffer []byte, length int) {
		// TODO: Consider adding:
		//	- Timestamp.
		//	- Fragmentation flag (Whether there is a half message or not).
		//	- Message count.

		cidBuilder := cid.V1Builder{
			Codec:    uint64(multicodec.DagPb),
			MhType:   uint64(multicodec.Sha2_256),
			MhLength: -1,
		}

		c, err := cidBuilder.Sum(buffer[0:length])
		if err != nil {
			// If this fails to parse a buffer the input is invalid.
			panic(err)
		}

		summary.DataHashes = append(summary.DataHashes, c)
		log.Info("Added ", length, "  bytes, now: "+c.String())
	}

	bufferOffset := 0
	printedDebug := false
	paused := false
	log.Info("Running for loop.")
	for {
		log.Info("Polling.")
		if paused {
			select {
			case <-ctx.Done():
				// XXX: This is duplicated, not sure if there's a simple way to handle this.
				log.Info("Context cancelled.")
				return
			case <-cw.resume:
				log.Info("Worker resumed.")
				paused = false
				break
			}
		} else {
			select {
			case <-ctx.Done():
				log.Info("Context cancelled.")
				return
			case <-cw.pause:
				log.Info("Channel stopped.")

				// Flush the buffer!
				if len(buffer) > 0 {
					summaryAppend(cw.summary, buffer, len(buffer))
				}

				// Wait until resume.
				log.Info("Worker paused until resume is called.")
				paused = true
				break
			case message := <-cw.message:
				// XXX: This looks ugly, whatever.
				if len(message) == 0 {
					if !printedDebug {
						log.Info("Got message with length 0, that means we probs disconnected :(")
					}
					printedDebug = true
					break
				}

				if bufferOffset+len(message) > len(buffer) {

					// TODO: Add to Resource Pool at this stage?
					summaryAppend(cw.summary, buffer, bufferOffset)
					bufferOffset = 0
				}

				// If the message still doesn't fit, divide it into chunks and add it until it fits.
				for len(message) > len(buffer) {
					// XXX: Should the cids we post be capped at the length of the buffer?
					// Or can they be any size? For now I assume they are capped at the size of the buffer.
					// Do we do padding? Need a spreadsheet to "empirically" test this.
					summaryAppend(cw.summary, message, len(buffer))
					message = message[len(buffer):]
				}

				// Add message to buffer.
				copy(buffer[bufferOffset:], message)
				// log.Info("Done here.")

				bufferOffset += len(message)
			}
		}
	}
}

func (ci *CollectorInstance) Start(ctx context.Context) {
	log.Infof("Started collector instance.")
	ci.ctx = ctx

	for i := range ci.workers {
		buffer := make([]byte, 4096)
		ci.workers[i].pause = make(chan bool)
		ci.workers[i].resume = make(chan bool)
		ci.workers[i].message = make(chan []byte)
		ci.workers[i].summary = &ci.summariesNew[i]
		runFunc := func() { ci.workers[i].run(ci.ctx, buffer) }

		log.Infof("Deploying worker for collector.")
		ci.workerWaitGroup.Go(runFunc)
	}
}

func (ci *CollectorInstance) Stop() {
	// This only works if the context was cancelled, otherwise the worker goroutines will block this.
	ci.workerWaitGroup.Wait()
}
