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
	Created  time.Time `json:"created,omitempty"`
	Modified time.Time `json:"modified,omitempty"`

	// comments about this user
	Comment string `json:"comment,omitempty"`

	// ip address
	IP string `json:"ip,omitempty"`

	// geo location
	Location Location `json:"location,omitempty" gorm:"type:jsonb"`

	// reported status
	Status Status `json:"status,omitempty"`

	// whether the user account has been disabled
	Disabled bool `json:"disabled,omitempty"`
}

func (self *User) TableName() string {
	return "user"
}
