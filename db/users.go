package db

import (
	"errors"
	"time"

	"github.com/jinzhu/gorm"
)

func (self *database) ListUsers() ([]*User, error) {
	var results []*User
	if err := self.db.Transaction(func(tx *gorm.DB) error {
		var models []User
		if err := tx.Find(&models).Error; err != nil {
			return err
		}
		results = make([]*User, 0, len(models))
		for i, _ := range models {
			results = append(results, &models[i])
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return results, nil
}

func (self *database) GetUser(id string) (*User, error) {
	var result *User
	if err := self.db.Transaction(func(tx *gorm.DB) error {
		var model User
		if err := tx.Where("id = ?", id).First(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		result = &model
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func (self *database) PutUser(id string, modifier func(*User) error) (*User, error) {
	var result *User
	if err := self.db.Transaction(func(tx *gorm.DB) error {
		var create bool
		var model User
		if err := tx.Where("id = ?", id).First(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				create = true
			} else {
				return err
			}
		}
		if err := modifier(&model); err != nil {
			return err
		}
		model.ID = id
		now := time.Now().In(time.Local)
		if create {
			model.Created = now
		}
		model.Modified = now
		if create {
			if err := tx.Create(&model).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Save(&model).Error; err != nil {
				return err
			}
		}
		result = &model
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}
