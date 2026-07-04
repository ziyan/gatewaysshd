package migrations

import (
	"sort"
	"strings"
	"testing"
)

func TestMigrationsAreSortedAndPaired(t *testing.T) {
	loaded := Migrations()
	if len(loaded) == 0 {
		t.Fatal("expected at least one migration")
	}

	ids := make([]string, 0, len(loaded))
	for _, migration := range loaded {
		if strings.TrimSpace(migration.SQL) == "" {
			t.Fatalf("migration %s has empty forward sql", migration.ID)
		}
		if strings.TrimSpace(migration.ReverseSQL) == "" {
			t.Fatalf("migration %s has empty reverse sql", migration.ID)
		}
		ids = append(ids, migration.ID)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("expected migrations to be sorted, got %v", ids)
	}
	if ids[0] != "0000_initial" {
		t.Fatalf("expected first migration to be 0000_initial, got %s", ids[0])
	}
}
