package gateway

import (
	"time"

	"github.com/boltdb/bolt"
)

var (
	bucketUsers       = []byte("users")
	bucketConnections = []byte("connections")
	buckets           = [][]byte{
		bucketUsers,
		bucketConnections,
	}
)

type Database struct {
	db *bolt.DB
}

func OpenDatabase(filename string) (*Database, error) {
	db, err := bolt.Open(filename, 0600, &bolt.Options{
		Timeout: 1 * time.Second,
	})
	if err != nil {
		return nil, err
	}

	// create the buckets when open
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, bucket := range buckets {
			if _, err := tx.CreateBucketIfNotExists(bucket); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		db.Close()
		return nil, err
	}

	return &Database{
		db: db,
	}, nil
}

func (d *Database) Close() error {
	return d.db.Close()
}

type userModel struct {
	Name        string   `json:"name"`
	Connections []string `json:"connections"`
}

type connectionModel struct {
	User string `json:"user"`
}
