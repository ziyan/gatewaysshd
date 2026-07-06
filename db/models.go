package db

import (
	"database/sql/driver"
	"encoding/json"
	"time"
)

// shared location info
type Location struct {
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	TimeZone  string  `json:"timezone,omitempty"`
	Country   string  `json:"country,omitempty"`
	City      string  `json:"city,omitempty"`
}

func (self *Location) Scan(value interface{}) error {
	var location Location
	if raw, ok := value.([]byte); ok {
		if err := json.Unmarshal(raw, &location); err != nil {
			return err
		}
	}
	*self = location
	return nil
}

func (self Location) Value() (driver.Value, error) {
	return json.Marshal(self)
}

type Status json.RawMessage

func (self *Status) Scan(value interface{}) error {
	var message json.RawMessage
	if raw, ok := value.([]byte); ok {
		if err := json.Unmarshal(raw, &message); err != nil {
			return err
		}
	}
	*self = Status(message)
	return nil
}

func (self Status) Value() (driver.Value, error) {
	if len(self) == 0 {
		return nil, nil
	}
	return json.RawMessage(self).MarshalJSON()
}

func (self Status) MarshalJSON() ([]byte, error) {
	if len(self) == 0 {
		return nil, nil
	}
	return json.RawMessage(self).MarshalJSON()
}

// represents a user connected to gateway
type User struct {
	ID string `json:"id,omitempty" gorm:"primary_key:true"`

	// meta data
	CreatedAt  time.Time `json:"createdAt,omitempty"`
	ModifiedAt time.Time `json:"modifiedAt,omitempty"`

	// comments about this user
	Comment string `json:"comment,omitempty"`

	// ip address
	IP string `json:"ip,omitempty"`

	// geo location
	Location Location `json:"location,omitempty" gorm:"type:jsonb"`

	// reported status
	Status Status `json:"status,omitempty"`

	// whether the user is an administrator
	Administrator bool `json:"administrator,omitempty"`

	// screenshot
	Screenshot []byte `json:"-" gorm:"type:bytea"`

	// whether the user account has been disabled
	Disabled bool `json:"disabled,omitempty"`

	// the node the user last connected to, used for mesh tunneling
	NodeID string `json:"nodeId,omitempty"`

	// the last time the user was seen connected on any node; refreshed on a
	// heartbeat while connected, so a fresh value means online mesh-wide
	OnlineAt time.Time `json:"onlineAt,omitempty"`

	// whether the user is online, derived from OnlineAt, not saved in database
	Online bool `json:"online,omitempty" gorm:"-"`

	// current connections, not saved in database
	Connections interface{} `json:"connections,omitempty" gorm:"-"`
}

func (self *User) TableName() string {
	return "user"
}

// represents a gateway node participating in the mesh
type Node struct {
	ID string `json:"id,omitempty" gorm:"primary_key:true"`

	// meta data
	CreatedAt  time.Time `json:"createdAt,omitempty"`
	ModifiedAt time.Time `json:"modifiedAt,omitempty"`

	// address where peer nodes can reach this node
	Address string `json:"address,omitempty"`

	// host public key of this node in authorized key format, pinned by
	// dialing peers to verify the remote host
	HostPublicKey string `json:"hostPublicKey,omitempty"`

	// whether the node is online
	Online bool `json:"online,omitempty"`

	// when the online status last changed
	OnlineAt time.Time `json:"onlineAt,omitempty"`
}

func (self *Node) TableName() string {
	return "node"
}
