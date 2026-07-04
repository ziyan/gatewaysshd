package db_test

import (
	"testing"

	"github.com/ziyan/gatewaysshd/db"
	"github.com/ziyan/gatewaysshd/db/dbtest"
)

func TestPutNodeCreatesAndLists(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	if _, err := database.PutNode(t.Context(), "node-a", func(node *db.Node) error {
		node.Address = "127.0.0.1:2020"
		node.HostPublicKey = "ssh-ed25519 AAAA..."
		node.Online = true
		return nil
	}); err != nil {
		t.Fatalf("failed to create node: %s", err)
	}

	node, err := database.GetNode(t.Context(), "node-a")
	if err != nil {
		t.Fatalf("failed to get node: %s", err)
	}
	if node == nil || node.Address != "127.0.0.1:2020" || !node.Online {
		t.Fatalf("unexpected node: %+v", node)
	}

	// update marks it offline and preserves creation time
	updated, err := database.PutNode(t.Context(), "node-a", func(node *db.Node) error {
		node.Online = false
		return nil
	})
	if err != nil {
		t.Fatalf("failed to update node: %s", err)
	}
	if updated.Online {
		t.Fatalf("expected node to be offline, got %+v", updated)
	}

	nodes, err := database.ListNodes(t.Context())
	if err != nil {
		t.Fatalf("failed to list nodes: %s", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "node-a" {
		t.Fatalf("unexpected nodes: %+v", nodes)
	}
}

func TestGetNodeMissingReturnsNil(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	node, err := database.GetNode(t.Context(), "missing")
	if err != nil {
		t.Fatalf("failed to get missing node: %s", err)
	}
	if node != nil {
		t.Fatalf("expected nil, got %+v", node)
	}
}
