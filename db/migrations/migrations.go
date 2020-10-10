package migrations

import (
	"sort"
)

var migrations = make(map[string]string)

func addMigration(id, sql string) {
	migrations[id] = sql
}

type Migration struct {
	ID  string
	SQL string
}

// retrieve all registred migrations in this package
func Migrations() []Migration {
	ids := make([]string, 0, len(migrations))
	for id := range migrations {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	sortedMigrations := make([]Migration, 0, len(migrations))
	for _, id := range ids {
		sortedMigrations = append(sortedMigrations, Migration{
			ID:  id,
			SQL: migrations[id],
		})
	}
	return sortedMigrations
}
