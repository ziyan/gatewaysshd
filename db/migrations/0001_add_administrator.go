package migrations

func init() {
	addMigration("0001_add_administrator", `
		ALTER TABLE "user" ADD COLUMN "administrator" boolean;
	`)
}
