package migrations

func init() {
	addMigration("0000_initial", `
		CREATE TABLE IF NOT EXISTS "user" (
			"id" varchar(256),
			"created" timestamp with time zone,
			"modified" timestamp with time zone,
			"comment" text,
			"ip" varchar(256),
			"location" jsonb,
			"status" jsonb,
			"disabled" boolean,
			PRIMARY KEY ("id")
		);
	`)
}
