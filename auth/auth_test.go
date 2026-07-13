package auth

import (
	"fmt"
	"testing"
	"time"

	"github.com/ziyan/gatewaysshd/db"
)

func newTestAuthenticator() *authenticator {
	return &authenticator{
		settings:  &Settings{},
		userFlags: make(map[string]userFlags),
	}
}

func TestUserFlagsHitAndRevoke(t *testing.T) {
	authenticator := newTestAuthenticator()

	if _, ok := authenticator.getUserFlags("alice"); ok {
		t.Fatal("expected miss on empty cache")
	}

	authenticator.putUserFlags("alice", &db.User{Administrator: true})
	flags, ok := authenticator.getUserFlags("alice")
	if !ok || !flags.administrator || flags.disabled {
		t.Fatalf("expected administrator hit, got %+v %v", flags, ok)
	}

	// a kick revokes the flags so the next login re-reads the database
	authenticator.revokeUserFlags("alice")
	if _, ok := authenticator.getUserFlags("alice"); ok {
		t.Fatal("expected miss after revocation")
	}
}

func TestUserFlagsExpireAndAreDeleted(t *testing.T) {
	authenticator := newTestAuthenticator()
	authenticator.putUserFlags("alice", &db.User{Disabled: true})

	// age the entry past its expiry
	flags := authenticator.userFlags["alice"]
	flags.expiresAt = time.Now().Add(-time.Second)
	authenticator.userFlags["alice"] = flags

	if _, ok := authenticator.getUserFlags("alice"); ok {
		t.Fatal("expected expired entry to miss")
	}
	if _, ok := authenticator.userFlags["alice"]; ok {
		t.Fatal("expected expired entry to be deleted on read")
	}
}

func TestUserFlagsBoundsItsSize(t *testing.T) {
	authenticator := newTestAuthenticator()

	// filling with expired entries only evicts them on overflow
	for index := range userFlagsMaxEntries {
		userId := fmt.Sprintf("user%d", index)
		authenticator.putUserFlags(userId, &db.User{})
		flags := authenticator.userFlags[userId]
		flags.expiresAt = time.Now().Add(-time.Second)
		authenticator.userFlags[userId] = flags
	}
	authenticator.putUserFlags("alice", &db.User{})
	if len(authenticator.userFlags) != 1 {
		t.Fatalf("expected expired entries to be evicted, got %d", len(authenticator.userFlags))
	}

	// filling with fresh entries resets the cache wholesale on overflow
	for index := range userFlagsMaxEntries {
		authenticator.putUserFlags(fmt.Sprintf("user%d", index), &db.User{})
	}
	authenticator.putUserFlags("bob", &db.User{})
	if len(authenticator.userFlags) > userFlagsMaxEntries {
		t.Fatalf("expected the cache to stay bounded, got %d", len(authenticator.userFlags))
	}
	if _, ok := authenticator.getUserFlags("bob"); !ok {
		t.Fatal("expected the newest entry to survive")
	}
}
