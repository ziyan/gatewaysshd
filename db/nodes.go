package db

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

func (self *database) ListNodes(ctx context.Context) ([]*Node, error) {
	var results []*Node
	if err := self.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var models []Node
		if err := tx.Find(&models).Error; err != nil {
			return err
		}
		results = make([]*Node, 0, len(models))
		for index := range models {
			results = append(results, &models[index])
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return results, nil
}

func (self *database) GetNode(ctx context.Context, nodeId string) (*Node, error) {
	var result *Node
	if err := self.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model Node
		if err := tx.Where("id = ?", nodeId).First(&model).Error; err != nil {
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

func (self *database) PutNode(ctx context.Context, nodeId string, modifier func(*Node) error) (*Node, error) {
	var result *Node
	if err := self.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var create bool
		var model Node
		if err := tx.Where("id = ?", nodeId).First(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				create = true
			} else {
				return err
			}
		}
		if err := modifier(&model); err != nil {
			return err
		}
		model.ID = nodeId
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
