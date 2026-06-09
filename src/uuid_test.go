package main

import (
	"strconv"
	"strings"
	"testing"
)

// embeddedMillis decodes the first 48 bits of a UUID (the v7 timestamp field).
func embeddedMillis(t *testing.T, id string) int64 {
	t.Helper()
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Fatalf("malformed uuid: %q", id)
	}
	ms, err := strconv.ParseInt(parts[0]+parts[1], 16, 64)
	if err != nil {
		t.Fatalf("parse ts hex: %v", err)
	}
	return ms
}

func TestToV7EmbedsTimestamp(t *testing.T) {
	// 2026-06-09T08:25:29.754Z
	const ms int64 = 1780993529754
	id := toV7("trace:abc", ms)

	if got := embeddedMillis(t, id); got != ms {
		t.Errorf("embedded timestamp = %d, want %d", got, ms)
	}
	// version nibble must be 7 (first char of 3rd group)
	if v := strings.Split(id, "-")[2][0]; v != '7' {
		t.Errorf("version nibble = %c, want 7", v)
	}
	// variant nibble must be 8/9/a/b (first char of 4th group)
	if c := strings.Split(id, "-")[3][0]; !strings.ContainsRune("89ab", rune(c)) {
		t.Errorf("variant nibble = %c, want one of 89ab", c)
	}
}

func TestToV7Deterministic(t *testing.T) {
	const ms int64 = 1780993529754
	a := toV7("trace:session:hash:42", ms)
	b := toV7("trace:session:hash:42", ms)
	if a != b {
		t.Errorf("same key+ts must be deterministic: %s != %s", a, b)
	}
	// Different key → different tail (entropy bytes), same timestamp prefix.
	c := toV7("trace:session:hash:43", ms)
	if c == a {
		t.Errorf("different keys collided: %s", c)
	}
	if embeddedMillis(t, c) != ms {
		t.Errorf("timestamp prefix changed with key")
	}
}

func TestMillisFromISO(t *testing.T) {
	cases := map[string]int64{
		"2026-06-09T08:25:29.754Z": 1780993529754,
		"2026-06-09T08:25:29Z":     1780993529000,
		"not-a-time":               0,
	}
	for in, want := range cases {
		if got := millisFromISO(in); got != want {
			t.Errorf("millisFromISO(%q) = %d, want %d", in, got, want)
		}
	}
}
