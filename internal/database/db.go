package database

import (
	"fmt"

	"github.com/dgraph-io/badger/v3"
	"github.com/openmesh-network/core/internal/logger"
)

// Instance is the instance that holds the database connection
type Instance struct {
	Conn *badger.DB
}

func NewInstance() (*Instance, error) {
	i := &Instance{}
	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true))

	i.Conn = db
	if err != nil {
		logger.Fatalf("Opening database: %v", err)
	}
	return i, nil
}

// Storage a chunk of messages from a CID in a SQL DB as key: CID and value: message bytes (BSON?).
func (datastore *Instance) storeMessageChunk(cid string, chunk []byte) {

	// Needs to be changed
	fmt.Printf("Storing chunk of messages with CID: %s\n", cid)
	dbTransaction := datastore.Conn.NewTransaction(true)
	dbTransaction.Set([]byte("cid"), chunk) // store bytes of CID alongside the associated chunk of messages.

	err := datastore.Conn.Update(func(txn *badger.Txn) error {
		// Your code here…
		return nil
	})
	if err != nil {
		panic(err)
	}

}

/*
func updateMessageChunk(txt *badger.Txn) error {
	return error.Error("No")
}
*/
