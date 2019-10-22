package gateway

import (
	"encoding/json"
	"time"

	"github.com/boltdb/bolt"
)

var (
	bucketUsers = []byte("users")
	buckets     = [][]byte{
		bucketUsers,
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
	// id of the user
	ID string `json:"id"`

	// ip address and geo location
	Address  string                 `json:"address"`
	Location map[string]interface{} `json:"location"`

	// reported status
	Status json.RawMessage `json:"status"`

	// last used timestamp
	Used int64 `json:"used"`
}

func (d *Database) updateUser(user *userModel) error {
	if err := d.db.Update(func(tx *bolt.Tx) error {
		model := &userModel{
			ID:       user.ID,
			Address:  user.Address,
			Location: user.Location,
			Status:   user.Status,
			Used:     user.Used,
		}

		// get user
		if raw := tx.Bucket(bucketUsers).Get([]byte(user.ID)); raw != nil {
			if err := json.Unmarshal(raw, &model); err != nil {
				return err
			}

			// update user
			model.ID = user.ID
			model.Address = user.Address
			if user.Location != nil {
				model.Location = user.Location
			}
			if user.Status != nil {
				model.Status = user.Status
			}
			model.Used = user.Used
		}

		// encode user
		raw, err := json.Marshal(model)
		if err != nil {
			return err
		}

		// save user
		if err := tx.Bucket(bucketUsers).Put([]byte(user.ID), raw); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (d *Database) listUsers() ([]*userModel, error) {
	var results []*userModel

	if err := d.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketUsers).Cursor()
		var models []*userModel
		for id, raw := cursor.First(); id != nil; id, raw = cursor.Next() {
			var model *userModel
			if err := json.Unmarshal(raw, &model); err != nil {
				return err
			}
			models = append(models, model)
		}

		results = models
		return nil
	}); err != nil {
		return nil, err
	}
	return results, nil
}
