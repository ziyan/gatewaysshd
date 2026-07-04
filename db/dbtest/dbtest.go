// Package dbtest provides helpers for database integration tests.
//
// Tests call AcquireDatabase to get an isolated, freshly migrated database.
// When the GATEWAYSSHD_TEST_DATABASE_HOST environment variable is not set (no
// postgres available), the helpers skip the test rather than fail it, so
// `go test ./...` still passes on a machine without docker.
package dbtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	logging "github.com/op/go-logging"
	"github.com/segmentio/ksuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/ziyan/gatewaysshd/db"
)

var log = logging.MustGetLogger("dbtest") //nolint:unused

// TestDatabaseHostEnv is the environment variable holding the postgres host to
// run integration tests against. The Makefile test target sets it to the IP of
// the postgres container it launches.
const TestDatabaseHostEnv = "GATEWAYSSHD_TEST_DATABASE_HOST"

const (
	testDatabasePort     = 5432
	testDatabaseUser     = "gatewaysshd"
	testDatabasePassword = "gatewaysshd"

	// adminConnectTimeoutSeconds bounds a single admin-database dial so a
	// half-open postgres handshake fails fast and is retried instead of
	// blocking on net.Read. A freshly launched container can accept TCP (and
	// pass pg_isready) while its init is still running.
	adminConnectTimeoutSeconds = 10
	// adminConnectBudget bounds how long execAdmin retries transient startup
	// failures (postgres still booting / in recovery).
	adminConnectBudget = 60 * time.Second
	adminRetryInterval = 500 * time.Millisecond
)

// databaseSerial advances per AcquireDatabase call so two tests sampling ksuid
// in the same instant never collide on the postgres database name. The pid
// prefix differentiates parallel `go test` shards.
var databaseSerial atomic.Uint64

func closeDatabase(database *gorm.DB) error {
	sqlDatabase, err := database.DB()
	if err != nil {
		return err
	}
	return sqlDatabase.Close()
}

// execAdmin runs a single statement against the postgres admin database,
// retrying transient startup failures up to adminConnectBudget. A connect
// failure is always transient at startup (TCP refused, handshake stalled, or
// server in recovery); an exec failure is retried only for recoverable
// SQLSTATEs and otherwise surfaces immediately.
func execAdmin(host, query string) error {
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=postgres sslmode=disable connect_timeout=%d",
		host, testDatabasePort, testDatabaseUser, testDatabasePassword, adminConnectTimeoutSeconds,
	)
	deadline := time.Now().Add(adminConnectBudget)
	for {
		database, openErr := gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if openErr != nil {
			if time.Now().After(deadline) {
				return fmt.Errorf("dbtest: connect to admin database: %w", openErr)
			}
			<-time.After(adminRetryInterval)
			continue
		}
		execErr := database.Exec(query).Error
		closeErr := closeDatabase(database)
		if execErr == nil {
			return closeErr
		}
		if isTransientExecError(execErr) && time.Now().Before(deadline) {
			<-time.After(adminRetryInterval)
			continue
		}
		return execErr
	}
}

// isTransientExecError reports whether an exec against the admin database should
// be retried: 57P03 (server entered recovery while still starting up).
func isTransientExecError(err error) bool {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		return pgError.Code == "57P03"
	}
	return false
}

// AcquireDatabase provisions a fresh, migrated database for the given test and
// returns it with a cleanup func that closes the connection and drops the
// database. The test is skipped when GATEWAYSSHD_TEST_DATABASE_HOST is not set.
func AcquireDatabase(test *testing.T) (db.Database, func()) {
	test.Helper()
	database, _, release := AcquireDatabaseWithSettings(test)
	return database, release
}

// AcquireDatabaseWithSettings is AcquireDatabase but also returns the
// connection settings, so tests can open additional connections to the same
// database (e.g. tunneled through a peer node).
func AcquireDatabaseWithSettings(test *testing.T) (db.Database, *db.Settings, func()) {
	test.Helper()

	host := os.Getenv(TestDatabaseHostEnv)
	if host == "" {
		test.Skipf("environment variable %s is not set, skipping tests that require database", TestDatabaseHostEnv)
	}

	databaseName := fmt.Sprintf("gatewaysshdtest%d_%d_%s", os.Getpid(), databaseSerial.Add(1), ksuid.New().String())

	if err := execAdmin(host, fmt.Sprintf("CREATE DATABASE %q", databaseName)); err != nil {
		test.Fatalf("failed to create test database: %s", err)
	}
	settings := &db.Settings{
		Host:         host,
		Port:         testDatabasePort,
		User:         testDatabaseUser,
		Password:     testDatabasePassword,
		DatabaseName: databaseName,
	}
	database, err := db.Open(settings)
	if err != nil {
		test.Fatalf("failed to open test database: %s", err)
	}
	if err := database.Migrate(context.Background()); err != nil {
		test.Fatalf("failed to migrate test database: %s", err)
	}
	return database, settings, func() {
		if err := database.Close(); err != nil {
			test.Fatalf("failed to close test database: %s", err)
		}
		// WITH (FORCE) because db.Database.Close does not tear down the
		// underlying gorm connection pool.
		if err := execAdmin(host, fmt.Sprintf("DROP DATABASE %q WITH (FORCE)", databaseName)); err != nil {
			test.Fatalf("failed to drop test database: %s", err)
		}
	}
}
