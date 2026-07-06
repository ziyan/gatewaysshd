package db_test

import (
	"encoding/json"
	"reflect"
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

	// a live node and a dead one, for the node_id adoption rule
	seedNode := func(nodeId string, onlineAt time.Time) {
		if _, err := database.PutNode(t.Context(), nodeId, func(node *db.Node) error {
			node.Online = true
			node.OnlineAt = onlineAt
			return nil
		}); err != nil {
			t.Fatalf("failed to seed node %s: %s", nodeId, err)
		}
	}
	seedNode("node-live", time.Now())
	seedNode("node-dead", time.Now().Add(-time.Hour))

	// alice sits on a live node, bob never comes online, carol's node died,
	// dave never had a node
	for name, nodeId := range map[string]string{"alice": "node-live", "bob": "node-live", "carol": "node-dead", "dave": ""} {
		if _, err := database.PutUser(t.Context(), name, func(user *db.User) error {
			user.NodeID = nodeId
			return nil
		}); err != nil {
			t.Fatalf("failed to create user %s: %s", name, err)
		}
	}

	// empty id set is a no-op
	if err := database.MarkUsersOnline(t.Context(), nil, "node-y", time.Minute); err != nil {
		t.Fatalf("expected nil for empty ids, got %v", err)
	}

	// node-y heartbeats alice, carol and dave; bob stays offline
	if err := database.MarkUsersOnline(t.Context(), []string{"alice", "carol", "dave"}, "node-y", time.Minute); err != nil {
		t.Fatalf("failed to mark users online: %s", err)
	}

	users, err := database.ListOnlineUsers(t.Context(), time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("failed to list online users: %s", err)
	}
	nodes := make(map[string]string)
	for _, user := range users {
		if user.OnlineAt.IsZero() {
			t.Fatalf("expected onlineAt to be set for %s", user.ID)
		}
		nodes[user.ID] = user.NodeID
	}
	// alice keeps her live node, carol and dave are adopted by node-y
	want := map[string]string{"alice": "node-live", "carol": "node-y", "dave": "node-y"}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("unexpected online users: %+v, want %+v", nodes, want)
	}

	// a since threshold newer than the heartbeat filters everyone out
	if users, err := database.ListOnlineUsers(t.Context(), time.Now().Add(time.Minute)); err != nil || len(users) != 0 {
		t.Fatalf("expected no online users past the threshold, got %+v err %v", users, err)
	}

	// a node not in the mesh refreshes online_at but never touches node_id
	if err := database.MarkUsersOnline(t.Context(), []string{"dave"}, "", time.Minute); err != nil {
		t.Fatalf("failed to mark users online without a node: %s", err)
	}
	if user, err := database.GetUser(t.Context(), "dave"); err != nil || user.NodeID != "node-y" {
		t.Fatalf("expected node_id to survive a non-mesh heartbeat, got %+v err %v", user, err)
	}
}

func TestClearUserNodeID(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	if _, err := database.PutUser(t.Context(), "alice", func(user *db.User) error {
		user.NodeID = "node-a"
		return nil
	}); err != nil {
		t.Fatalf("failed to create user: %s", err)
	}

	// clearing on behalf of another node is a no-op
	if err := database.ClearUserNodeID(t.Context(), "alice", "node-b"); err != nil {
		t.Fatalf("failed to clear node: %s", err)
	}
	if user, err := database.GetUser(t.Context(), "alice"); err != nil || user.NodeID != "node-a" {
		t.Fatalf("expected node_id to be kept, got %+v err %v", user, err)
	}

	// clearing by the owning node releases the user
	if err := database.ClearUserNodeID(t.Context(), "alice", "node-a"); err != nil {
		t.Fatalf("failed to clear node: %s", err)
	}
	if user, err := database.GetUser(t.Context(), "alice"); err != nil || user.NodeID != "" {
		t.Fatalf("expected node_id to be cleared, got %+v err %v", user, err)
	}
}
