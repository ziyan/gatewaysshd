package db

import (
	"context"
	"database/sql"
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

// userDetailColumns adds the status blob for the single-user queries; only
// the screenshot is left out, it is served by GetUserScreenshot alone.
var userDetailColumns = append(append([]string{}, userListColumns...), "status")

// listUsers returns the users matching the optional conditions, selecting
// only userListColumns. No transaction wrapper: a single statement is atomic
// on its own, and the extra BEGIN/COMMIT roundtrips are costly when the
// database is reached through a cross-region peer tunnel.
func (self *database) listUsers(ctx context.Context, conditions ...interface{}) ([]*User, error) {
	var models []User
	if err := self.db.WithContext(ctx).Select(userListColumns).Find(&models, conditions...).Error; err != nil {
		return nil, err
	}
	results := make([]*User, 0, len(models))
	for index := range models {
		results = append(results, &models[index])
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

// GetUsers returns only the users with the given ids, filtered in sql, so
// callers that already know the subset do not load the whole table.
func (self *database) GetUsers(ctx context.Context, userIds []string) ([]*User, error) {
	if len(userIds) == 0 {
		return nil, nil
	}
	return self.listUsers(ctx, "id IN ?", userIds)
}

// userNodeGone matches a user whose recorded node is absent or has a stale
// heartbeat, so the beating node may adopt them. Takes one parameter: the
// time before which a node heartbeat counts as stale.
const userNodeGone = `(node_id IS NULL OR node_id = '' OR node_id NOT IN (SELECT id FROM "node" WHERE online AND online_at > ?))`

// MarkUsersOnline refreshes online_at for the given users, called on a
// heartbeat by a node they are connected to. node_id is only adopted when the
// user's current node is gone (unset, or its own heartbeat is older than
// nodeStaleAfter): concurrent heartbeats from several nodes then leave a
// multi-node user's node_id stable instead of fighting over it, while a node
// that crashed or released the user still gets replaced. A single statement,
// so the periodic heartbeat costs one roundtrip and holds no locks across
// the cross-region tunnel.
func (self *database) MarkUsersOnline(ctx context.Context, userIds []string, nodeId string, nodeStaleAfter time.Duration) error {
	if len(userIds) == 0 {
		return nil
	}
	now := time.Now().In(time.Local)
	if nodeId == "" {
		// this node is not in the mesh, only refresh the heartbeat
		return self.db.WithContext(ctx).Model(&User{}).Where("id IN ?", userIds).Update("online_at", now).Error
	}
	// every SET expression sees the old row, so both CASEs test the same state
	staleBefore := now.Add(-nodeStaleAfter)
	return self.db.WithContext(ctx).Exec(
		`UPDATE "user" SET online_at = ?, `+
			`modified_at = CASE WHEN `+userNodeGone+` THEN ? ELSE modified_at END, `+
			`node_id = CASE WHEN `+userNodeGone+` THEN ? ELSE node_id END `+
			`WHERE id IN ?`,
		now, staleBefore, now, staleBefore, nodeId, userIds,
	).Error
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
	var model User
	if err := self.db.WithContext(ctx).Select(userDetailColumns).Where("id = ?", userId).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &model, nil
}

// GetUserScreenshot fetches only the screenshot blob, nil when the user is
// unknown or never reported one.
func (self *database) GetUserScreenshot(ctx context.Context, userId string) ([]byte, error) {
	var screenshots [][]byte
	if err := self.db.WithContext(ctx).Model(&User{}).Where("id = ?", userId).Limit(1).Pluck("screenshot", &screenshots).Error; err != nil {
		return nil, err
	}
	if len(screenshots) == 0 {
		return nil, nil
	}
	return screenshots[0], nil
}

// GetUserNodeID returns the node the user last connected to, or empty when
// the user is unknown. It fetches a single column so the mesh tunnel routing
// path costs one small roundtrip instead of pulling the whole row with its
// status and screenshot blobs.
func (self *database) GetUserNodeID(ctx context.Context, userId string) (string, error) {
	var nodeId string
	err := self.db.WithContext(ctx).Raw(`SELECT coalesce(node_id, '') FROM "user" WHERE id = ?`, userId).Row().Scan(&nodeId)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return nodeId, nil
}

// UpsertUserOnConnect records a login in a single statement: it creates the
// user on first connect and refreshes ip, location and presence (node_id,
// online_at). Presence is kept unchanged for a disabled user, whose login is
// rejected right after and must not appear online. A single statement keeps
// the authentication path at one database roundtrip; the returned user
// reflects the stored row for all light columns (RETURNING), only the status
// and screenshot blobs are intentionally not fetched.
func (self *database) UpsertUserOnConnect(ctx context.Context, userId string, ip string, location Location, nodeId string) (*User, error) {
	now := time.Now().In(time.Local)
	model := User{
		ID:         userId,
		CreatedAt:  now,
		ModifiedAt: now,
		IP:         ip,
		Location:   location,
		NodeID:     nodeId,
		OnlineAt:   now,
	}
	returningColumns := make([]clause.Column, 0, len(userListColumns))
	for _, column := range userListColumns {
		returningColumns = append(returningColumns, clause.Column{Name: column})
	}
	if err := self.db.WithContext(ctx).Clauses(
		clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"modified_at": now,
				"ip":          ip,
				"location":    location,
				"node_id":     gorm.Expr(`CASE WHEN "user".disabled THEN "user".node_id ELSE excluded.node_id END`),
				"online_at":   gorm.Expr(`CASE WHEN "user".disabled THEN "user".online_at ELSE excluded.online_at END`),
			}),
		},
		clause.Returning{Columns: returningColumns},
	).Create(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// updateUserColumn saves a single column of an existing user, one small
// statement for the frequently-called reporting paths. A report for a user
// row that vanished is an error instead of a silent no-op.
func (self *database) updateUserColumn(ctx context.Context, userId string, column string, value interface{}) error {
	result := self.db.WithContext(ctx).Model(&User{}).Where("id = ?", userId).Updates(map[string]interface{}{
		column:        value,
		"modified_at": time.Now().In(time.Local),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// UpdateUserStatus saves only the reported status.
func (self *database) UpdateUserStatus(ctx context.Context, userId string, status Status) error {
	return self.updateUserColumn(ctx, userId, "status", status)
}

// UpdateUserScreenshot saves only the reported screenshot, without dragging
// the status blob along.
func (self *database) UpdateUserScreenshot(ctx context.Context, userId string, screenshot []byte) error {
	return self.updateUserColumn(ctx, userId, "screenshot", screenshot)
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
