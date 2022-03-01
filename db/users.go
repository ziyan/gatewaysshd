package db

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

func (self *database) ListUsers() ([]*User, error) {
	var results []*User
	if err := self.db.Transaction(func(tx *gorm.DB) error {
		var models []User
		if err := tx.Select([]string{
			"id",
			"created_at",
			"modified_at",
			"comment",
			"ip",
			"location",
			"administrator",
			"disabled",
		}).Find(&models).Error; err != nil {
			return err
		}
		results = make([]*User, 0, len(models))
		for index := range models {
			results = append(results, &models[index])
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return results, nil
}

func (self *database) GetUser(userId string) (*User, error) {
	var result *User
	if err := self.db.Transaction(func(tx *gorm.DB) error {
		var model User
		if err := tx.Where("id = ?", userId).First(&model).Error; err != nil {
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

func (self *database) PutUser(userId string, modifier func(*User) error) (*User, error) {
	var result *User
	if err := self.db.Transaction(func(tx *gorm.DB) error {
		var create bool
		var model User
		if err := tx.Where("id = ?", userId).First(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				create = true
			} else {
				return err
			}
		}
		if err := modifier(&model); err != nil {
			return err
		}
		model.ID = userId
		now := time.Now().In(time.Local)
		if create {
			model.CreatedAt = now
		}
		model.ModifiedAt = now
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
