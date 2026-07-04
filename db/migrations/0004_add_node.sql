CREATE TABLE IF NOT EXISTS "node" (
    "id" varchar(256),
    "created_at" timestamp with time zone,
    "modified_at" timestamp with time zone,
    "address" varchar(256),
    "host_public_key" text,
    "online" boolean,
    "online_at" timestamp with time zone,
    PRIMARY KEY ("id")
);
