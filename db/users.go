package db

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// userListColumns are the columns returned by the user listing queries; the
// heavy status/screenshot columns are intentionally omitted.
var userListColumns = []string{
	"id",
	"created_at",
	"modified_at",
	"comment",
	"ip",
	"location",
	"administrator",
	"disabled",
	"node_id",
	"online_at",
}

func (self *database) ListUsers(ctx context.Context) ([]*User, error) {
	var results []*User
	if err := self.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var models []User
		if err := tx.Select(userListColumns).Find(&models).Error; err != nil {
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

// ListOnlineUsers returns the users whose online_at is newer than since,
// filtered in sql. Online status is mesh-wide: any node refreshing a user's
// online_at (see MarkUsersOnline) makes it visible from every node.
func (self *database) ListOnlineUsers(ctx context.Context, since time.Time) ([]*User, error) {
	var results []*User
	if err := self.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var models []User
		if err := tx.Select(userListColumns).Where("online_at > ?", since).Find(&models).Error; err != nil {
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

// MarkUsersOnline refreshes online_at and node_id for the given users, called
// on a heartbeat by the node they are connected to. Refreshing node_id keeps
// it pointing at a node the user is currently on, self-healing the stale value
// left behind when a user leaves the node it last authenticated to.
func (self *database) MarkUsersOnline(ctx context.Context, ids []string, nodeId string, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return self.db.WithContext(ctx).Model(&User{}).Where("id IN ?", ids).Updates(map[string]interface{}{
		"online_at": at,
		"node_id":   nodeId,
	}).Error
}

func (self *database) GetUser(ctx context.Context, userId string) (*User, error) {
	var result *User
	if err := self.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

func (self *database) PutUser(ctx context.Context, userId string, modifier func(*User) error) (*User, error) {
	var result *User
	if err := self.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
