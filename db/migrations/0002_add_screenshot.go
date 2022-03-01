package migrations

func init() {
	addMigration("0002_add_screenshot", `
		ALTER TABLE "user" ADD COLUMN "screenshot" bytea;
	`)
}
