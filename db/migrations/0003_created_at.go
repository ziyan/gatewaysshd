package migrations

func init() {
	addMigration("0003_created_at", `
		ALTER TABLE "user" RENAME COLUMN "created" TO "created_at";
		ALTER TABLE "user" RENAME COLUMN "modified" TO "modified_at";
	`)
}
