package db

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// listUsers returns the users matching the optional conditions, selecting
// only userListColumns.
func (self *database) listUsers(ctx context.Context, conditions ...interface{}) ([]*User, error) {
	var results []*User
	if err := self.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var models []User
		if err := tx.Select(userListColumns).Find(&models, conditions...).Error; err != nil {
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

func (self *database) ListUsers(ctx context.Context) ([]*User, error) {
	return self.listUsers(ctx)
}

// ListOnlineUsers returns the users whose online_at is newer than since,
// filtered in sql. Online status is mesh-wide: any node refreshing a user's
// online_at (see MarkUsersOnline) makes it visible from every node.
func (self *database) ListOnlineUsers(ctx context.Context, since time.Time) ([]*User, error) {
	return self.listUsers(ctx, "online_at > ?", since)
}

// MarkUsersOnline refreshes online_at for the given users, called on a
// heartbeat by a node they are connected to. node_id is only adopted when the
// user's current node is gone (unset, or its own heartbeat is older than
// nodeStaleAfter): concurrent heartbeats from several nodes then leave a
// multi-node user's node_id stable instead of fighting over it, while a node
// that crashed or released the user still gets replaced.
func (self *database) MarkUsersOnline(ctx context.Context, userIds []string, nodeId string, nodeStaleAfter time.Duration) error {
	if len(userIds) == 0 {
		return nil
	}
	now := time.Now().In(time.Local)
	return self.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&User{}).Where("id IN ?", userIds).Update("online_at", now).Error; err != nil {
			return err
		}
		if nodeId == "" {
			return nil // this node is not in the mesh, nothing to adopt
		}
		liveNodes := tx.Model(&Node{}).Select("id").Where("online AND online_at > ?", now.Add(-nodeStaleAfter))
		return tx.Model(&User{}).
			Where("id IN ? AND (node_id IS NULL OR node_id = '' OR node_id NOT IN (?))", userIds, liveNodes).
			Updates(map[string]interface{}{"node_id": nodeId, "modified_at": now}).Error
	})
}

// ClearUserNodeID clears the user's node_id if it still points at the given
// node, called when the user's last connection to that node closes so that a
// node the user is still connected to can adopt them on its next heartbeat.
func (self *database) ClearUserNodeID(ctx context.Context, userId string, nodeId string) error {
	return self.db.WithContext(ctx).Model(&User{}).
		Where("id = ? AND node_id = ?", userId, nodeId).
		Updates(map[string]interface{}{"node_id": "", "modified_at": time.Now().In(time.Local)}).Error
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
		// lock the row for the read-modify-write, otherwise the Save below
		// silently reverts a concurrent heartbeat's online_at/node_id update
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", userId).First(&model).Error; err != nil {
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
