package migrations

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	logging "github.com/op/go-logging"
)

var log = logging.MustGetLogger("migrations") //nolint:unused

//go:embed *.sql
var migrationFiles embed.FS

type Migration struct {
	ID         string
	SQL        string
	ReverseSQL string
}

var migrationCache = mustLoadMigrations()

// retrieve all registered migrations in this package
func Migrations() []Migration {
	return migrationCache
}

func mustLoadMigrations() []Migration {
	entries, err := fs.ReadDir(migrationFiles, ".")
	if err != nil {
		panic(fmt.Errorf("migrations: failed to read migrations: %w", err))
	}

	forward := make(map[string]string)
	reverse := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		content, err := migrationFiles.ReadFile(name)
		if err != nil {
			panic(fmt.Errorf("migrations: failed to read migration file %q: %w", name, err))
		}
		if strings.HasSuffix(name, ".reverse.sql") {
			migrationId := strings.TrimSuffix(name, ".reverse.sql")
			reverse[migrationId] = strings.TrimSpace(string(content))
			continue
		}
		migrationId := strings.TrimSuffix(name, ".sql")
		forward[migrationId] = strings.TrimSpace(string(content))
	}

	for migrationId := range forward {
		if _, ok := reverse[migrationId]; !ok {
			panic(fmt.Sprintf("migrations: missing reverse migration for %s", migrationId))
		}
	}
	for migrationId := range reverse {
		if _, ok := forward[migrationId]; !ok {
			panic(fmt.Sprintf("migrations: missing forward migration for %s", migrationId))
		}
	}

	migrationIds := make([]string, 0, len(forward))
	for migrationId := range forward {
		migrationIds = append(migrationIds, migrationId)
	}
	sort.Strings(migrationIds)

	sortedMigrations := make([]Migration, 0, len(migrationIds))
	for _, migrationId := range migrationIds {
		sortedMigrations = append(sortedMigrations, Migration{
			ID:         migrationId,
			SQL:        forward[migrationId],
			ReverseSQL: reverse[migrationId],
		})
	}
	return sortedMigrations
}
