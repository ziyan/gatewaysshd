package db_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ziyan/gatewaysshd/db"
	"github.com/ziyan/gatewaysshd/db/dbtest"
)

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	// AcquireDatabase already migrated once, a second run must be a no-op
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatalf("failed to migrate twice: %s", err)
	}
}

func TestPutUserCreatesAndGetUserRoundTrips(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	location := db.Location{
		Latitude:  35.6595,
		Longitude: 139.7005,
		TimeZone:  "Asia/Tokyo",
		Country:   "JP",
		City:      "Tokyo",
	}
	created, err := database.PutUser(t.Context(), "alice", func(user *db.User) error {
		user.Comment = "test user"
		user.IP = "203.0.113.7"
		user.Location = location
		user.Administrator = true
		user.Status = db.Status(`{"healthy":true}`)
		user.Screenshot = []byte{0x89, 0x50, 0x4e, 0x47}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user: %s", err)
	}
	if created.CreatedAt.IsZero() || created.ModifiedAt.IsZero() {
		t.Fatalf("expected timestamps to be set, got %+v", created)
	}

	user, err := database.GetUser(t.Context(), "alice")
	if err != nil {
		t.Fatalf("failed to get user: %s", err)
	}
	if user == nil {
		t.Fatal("expected user, got nil")
	}
	if user.Comment != "test user" || user.IP != "203.0.113.7" || !user.Administrator {
		t.Fatalf("unexpected user: %+v", user)
	}
	if user.Location != location {
		t.Fatalf("expected location %+v, got %+v", location, user.Location)
	}
	// jsonb normalizes whitespace, compare parsed content
	var status map[string]bool
	if err := json.Unmarshal(user.Status, &status); err != nil {
		t.Fatalf("failed to parse status %s: %s", user.Status, err)
	}
	if !status["healthy"] {
		t.Fatalf("unexpected status: %s", user.Status)
	}
	if len(user.Screenshot) != 4 {
		t.Fatalf("unexpected screenshot: %v", user.Screenshot)
	}
}

func TestGetUserMissingReturnsNil(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	user, err := database.GetUser(t.Context(), "missing")
	if err != nil {
		t.Fatalf("failed to get missing user: %s", err)
	}
	if user != nil {
		t.Fatalf("expected nil, got %+v", user)
	}
}

func TestPutUserUpdatesExistingUser(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	created, err := database.PutUser(t.Context(), "bob", func(user *db.User) error {
		user.Comment = "before"
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user: %s", err)
	}

	updated, err := database.PutUser(t.Context(), "bob", func(user *db.User) error {
		if user.Comment != "before" {
			t.Fatalf("modifier expected existing user, got %+v", user)
		}
		user.Comment = "after"
		return nil
	})
	if err != nil {
		t.Fatalf("failed to update user: %s", err)
	}
	if updated.Comment != "after" {
		t.Fatalf("expected updated comment, got %q", updated.Comment)
	}
	// postgres stores timestamps with microsecond precision
	if !updated.CreatedAt.Equal(created.CreatedAt.Truncate(time.Microsecond)) {
		t.Fatalf("expected creation time to be preserved, got %s != %s", updated.CreatedAt, created.CreatedAt)
	}

	user, err := database.GetUser(t.Context(), "bob")
	if err != nil {
		t.Fatalf("failed to get user: %s", err)
	}
	if user.Comment != "after" {
		t.Fatalf("expected persisted comment, got %q", user.Comment)
	}
}

func TestListUsersOmitsHeavyColumns(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	for _, name := range []string{"alice", "bob"} {
		if _, err := database.PutUser(t.Context(), name, func(user *db.User) error {
			user.Status = db.Status(`{"healthy":true}`)
			user.Screenshot = []byte{0x89}
			return nil
		}); err != nil {
			t.Fatalf("failed to create user %s: %s", name, err)
		}
	}

	users, err := database.ListUsers(t.Context())
	if err != nil {
		t.Fatalf("failed to list users: %s", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}
	for _, user := range users {
		// status and screenshot are intentionally not selected by ListUsers
		if len(user.Status) != 0 || len(user.Screenshot) != 0 {
			t.Fatalf("expected heavy columns to be omitted, got %+v", user)
		}
	}
}

func TestMarkUsersOnlineAndListOnlineUsers(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	for _, name := range []string{"alice", "bob", "carol"} {
		if _, err := database.PutUser(t.Context(), name, func(user *db.User) error {
			user.NodeID = "node-x"
			return nil
		}); err != nil {
			t.Fatalf("failed to create user %s: %s", name, err)
		}
	}

	now := time.Now()

	// empty id set is a no-op
	if err := database.MarkUsersOnline(t.Context(), nil, now); err != nil {
		t.Fatalf("expected nil for empty ids, got %v", err)
	}

	// mark alice and carol online now, bob stays offline
	if err := database.MarkUsersOnline(t.Context(), []string{"alice", "carol"}, now); err != nil {
		t.Fatalf("failed to mark users online: %s", err)
	}

	got, err := database.ListOnlineUsers(t.Context(), now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("failed to list online users: %s", err)
	}
	seen := make(map[string]bool)
	for _, user := range got {
		seen[user.ID] = true
		if user.NodeID != "node-x" {
			t.Fatalf("expected nodeId node-x for %s, got %q", user.ID, user.NodeID)
		}
		if user.OnlineAt.IsZero() {
			t.Fatalf("expected onlineAt to be set for %s", user.ID)
		}
	}
	if len(got) != 2 || !seen["alice"] || !seen["carol"] || seen["bob"] {
		t.Fatalf("unexpected online subset: %+v", seen)
	}

	// a since threshold newer than the heartbeat filters everyone out
	if got, err := database.ListOnlineUsers(t.Context(), now.Add(time.Minute)); err != nil || len(got) != 0 {
		t.Fatalf("expected no online users past the threshold, got %+v err %v", got, err)
	}
}
