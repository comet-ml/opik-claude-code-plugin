package main

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// uuid7 generates a new UUIDv7 with current timestamp and random bytes
func uuid7() string {
	ts := time.Now().UnixMilli()

	// Random bytes for the rest
	randBytes := make([]byte, 10)
	if _, err := rand.Read(randBytes); err != nil {
		// Fallback: use nanoseconds for entropy (extremely rare path)
		nanos := time.Now().UnixNano()
		for i := range randBytes {
			randBytes[i] = byte(nanos >> (i * 8))
		}
	}

	// Format: xxxxxxxx-xxxx-7xxx-yxxx-xxxxxxxxxxxx
	// First 48 bits: timestamp
	// Next 4 bits: version (7)
	// Next 12 bits: random
	// Next 2 bits: variant (10)
	// Next 62 bits: random

	tsHex := fmt.Sprintf("%012x", ts)
	randHex := hex.EncodeToString(randBytes)

	// Set variant bits (10xx) on byte 8
	varByte := (randBytes[2] & 0x3F) | 0x80
	varHex := fmt.Sprintf("%02x", varByte)

	return fmt.Sprintf("%s-%s-7%s-%s%s-%s",
		tsHex[0:8],
		tsHex[8:12],
		randHex[0:3],
		varHex,
		randHex[5:7],
		randHex[7:19])
}

// toV7 builds a deterministic UUIDv7 from an arbitrary key and a millisecond
// timestamp. The first 48 bits carry tsMillis (so the embedded time matches
// the entity's start_time and the ID sorts chronologically, like a real v7),
// while the remaining "random" bits are taken from MD5(key). Deriving the tail
// from the key keeps IDs deterministic — same key + same tsMillis always yields
// the same ID — which is what enables idempotent upserts and the duplicate-fire
// dedup guard in onPrompt.
//
// Callers must pass the SAME tsMillis whenever they recompute an ID for a given
// key (e.g. a parent span referenced from two call sites), or the IDs won't
// match. In practice the timestamp is always derived from the same immutable
// transcript entry, so this holds.
func toV7(key string, tsMillis int64) string {
	hash := md5.Sum([]byte(key))
	h := hex.EncodeToString(hash[:])

	// First 48 bits: timestamp (Unix milliseconds).
	tsHex := fmt.Sprintf("%012x", tsMillis&0xFFFFFFFFFFFF)

	// Set version to 7 (0111) in byte 6
	b6 := (hash[6] & 0x0F) | 0x70
	b6Hex := fmt.Sprintf("%02x", b6)

	// Set variant to 10 in byte 8
	b8 := (hash[8] & 0x3F) | 0x80
	b8Hex := fmt.Sprintf("%02x", b8)

	return fmt.Sprintf("%s-%s-%s%s-%s%s-%s",
		tsHex[0:8],
		tsHex[8:12],
		b6Hex,
		h[14:16],
		b8Hex,
		h[18:20],
		h[20:32])
}

// millisFromISO parses an ISO-8601 timestamp (as found in Claude Code
// transcripts and produced by isoNow) into Unix milliseconds. On parse failure
// it returns 0 — callers embed that into the v7 timestamp field, which is still
// deterministic for a given input string and so preserves ID stability.
func millisFromISO(s string) int64 {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}
