package db

import (
	"bytes"
	"testing"
)

func TestLocationScanValueRoundTrip(t *testing.T) {
	original := Location{
		Latitude:  35.6595,
		Longitude: 139.7005,
		TimeZone:  "Asia/Tokyo",
		Country:   "JP",
		City:      "Tokyo",
	}

	value, err := original.Value()
	if err != nil {
		t.Fatalf("failed to serialize location: %s", err)
	}

	var scanned Location
	if err := scanned.Scan(value); err != nil {
		t.Fatalf("failed to scan location: %s", err)
	}
	if scanned != original {
		t.Fatalf("expected %+v, got %+v", original, scanned)
	}
}

func TestLocationScanNil(t *testing.T) {
	scanned := Location{Country: "JP"}
	if err := scanned.Scan(nil); err != nil {
		t.Fatalf("failed to scan nil: %s", err)
	}
	if scanned != (Location{}) {
		t.Fatalf("expected zero location, got %+v", scanned)
	}
}

func TestStatusScanValueRoundTrip(t *testing.T) {
	original := Status(`{"healthy":true}`)

	value, err := original.Value()
	if err != nil {
		t.Fatalf("failed to serialize status: %s", err)
	}
	raw, ok := value.([]byte)
	if !ok {
		t.Fatalf("expected []byte value, got %T", value)
	}

	var scanned Status
	if err := scanned.Scan(raw); err != nil {
		t.Fatalf("failed to scan status: %s", err)
	}
	if !bytes.Equal(scanned, original) {
		t.Fatalf("expected %s, got %s", original, scanned)
	}
}

func TestStatusEmptyValueIsNil(t *testing.T) {
	value, err := Status(nil).Value()
	if err != nil {
		t.Fatalf("failed to serialize empty status: %s", err)
	}
	if value != nil {
		t.Fatalf("expected nil value, got %v", value)
	}

	marshaled, err := Status(nil).MarshalJSON()
	if err != nil {
		t.Fatalf("failed to marshal empty status: %s", err)
	}
	if marshaled != nil {
		t.Fatalf("expected nil json, got %s", marshaled)
	}
}
