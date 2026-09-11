package main

import (
	"regexp"
	"strconv"
	"testing"
	"time"
)

var uuidV7Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// embeddedMillis decodes the 48-bit UUIDv7 timestamp prefix.
func embeddedMillis(t *testing.T, id string) int64 {
	t.Helper()
	ms, err := strconv.ParseInt(id[0:8]+id[9:13], 16, 64)
	if err != nil {
		t.Fatalf("cannot parse timestamp bits of %s: %v", id, err)
	}
	return ms
}

func TestToV7IsDeterministic(t *testing.T) {
	a := toV7("8b1d7f2e-1111-4c3a-9c1e-000000000001", "2026-09-11T14:54:43.966Z")
	b := toV7("8b1d7f2e-1111-4c3a-9c1e-000000000001", "2026-09-11T14:54:43.966Z")
	if a != b {
		t.Fatalf("same inputs produced different ids: %s vs %s", a, b)
	}
	c := toV7("8b1d7f2e-1111-4c3a-9c1e-000000000002", "2026-09-11T14:54:43.966Z")
	if a == c {
		t.Fatalf("different transcript uuids produced the same id: %s", a)
	}
}

func TestToV7EmbedsEntryTimestamp(t *testing.T) {
	ts := "2026-09-11T14:54:43.966Z"
	want, _ := time.Parse(time.RFC3339Nano, ts)
	id := toV7("8b1d7f2e-1111-4c3a-9c1e-000000000001", ts)
	if !uuidV7Pattern.MatchString(id) {
		t.Fatalf("not a well-formed UUIDv7: %s", id)
	}
	if got := embeddedMillis(t, id); got != want.UnixMilli() {
		t.Fatalf("embedded timestamp %d, want %d (%s)", got, want.UnixMilli(), id)
	}
}

func TestToV7FallsBackToNowWithoutTimestamp(t *testing.T) {
	for _, ts := range []string{"", "not-a-timestamp"} {
		before := time.Now().UnixMilli()
		id := toV7("8b1d7f2e-1111-4c3a-9c1e-000000000001", ts)
		after := time.Now().UnixMilli()
		if !uuidV7Pattern.MatchString(id) {
			t.Fatalf("not a well-formed UUIDv7: %s", id)
		}
		got := embeddedMillis(t, id)
		if got < before || got > after {
			t.Fatalf("timestamp %q: embedded %d not within [%d, %d]", ts, got, before, after)
		}
	}
}

func TestUuid7EmbedsNow(t *testing.T) {
	before := time.Now().UnixMilli()
	id := uuid7()
	after := time.Now().UnixMilli()
	if !uuidV7Pattern.MatchString(id) {
		t.Fatalf("not a well-formed UUIDv7: %s", id)
	}
	got := embeddedMillis(t, id)
	if got < before || got > after {
		t.Fatalf("embedded %d not within [%d, %d]", got, before, after)
	}
}
