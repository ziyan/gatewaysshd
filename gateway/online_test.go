package gateway

import (
	"testing"
	"time"

	"github.com/ziyan/gatewaysshd/db"
)

func TestIsUserOnline(t *testing.T) {
	testCases := []struct {
		name     string
		onlineAt time.Time
		want     bool
	}{
		{"never seen", time.Time{}, false},
		{"fresh heartbeat", time.Now(), true},
		{"stale heartbeat", time.Now().Add(-onlineStaleThreshold - time.Second), false},
	}
	for _, testCase := range testCases {
		got := isUserOnline(&db.User{OnlineAt: testCase.onlineAt})
		if got != testCase.want {
			t.Fatalf("isUserOnline(%s) = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

func TestIsNodeOnline(t *testing.T) {
	testCases := []struct {
		name     string
		online   bool
		onlineAt time.Time
		want     bool
	}{
		{"online and fresh", true, time.Now(), true},
		{"crashed, row still online but heartbeat stale", true, time.Now().Add(-onlineStaleThreshold - time.Second), false},
		{"online but never beat", true, time.Time{}, false},
		{"cleanly shut down", false, time.Now(), false},
	}
	for _, testCase := range testCases {
		got := isNodeOnline(&db.Node{Online: testCase.online, OnlineAt: testCase.onlineAt})
		if got != testCase.want {
			t.Fatalf("isNodeOnline(%s) = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}
