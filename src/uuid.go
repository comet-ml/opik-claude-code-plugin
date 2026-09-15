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

// toV7 derives a deterministic UUIDv7 from a transcript UUID and the entry's
// timestamp.
//
// Determinism matters: the same spans are re-sent on every flush (PostToolUse,
// Stop, SessionEnd) and Opik upserts them by id, so a given transcript entry
// must always map to the same span id.
//
// The first 48 bits must be a real millisecond timestamp. Opik validates that
// a v7 id's embedded timestamp falls inside its ingestion window (24h around
// now) and rejects the whole batch otherwise. The previous implementation put
// MD5 hash bytes there, which decoded to timestamps centuries away and made
// every span batch fail with "Invalid UUID for id". Only the remaining 74 bits
// are derived from the hash now.
//
// timestamp is the transcript entry's RFC 3339 timestamp. If it is empty or
// unparseable the current time is used, which keeps the id valid but not
// deterministic across flushes; callers should pass the entry timestamp.
func toV7(uuid, timestamp string) string {
	ms := timestampMillis(timestamp)
	hash := md5.Sum([]byte(uuid))
	h := hex.EncodeToString(hash[:])

	// First 48 bits: unix milliseconds
	tsHex := fmt.Sprintf("%012x", ms&0xFFFFFFFFFFFF)

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

// timestampMillis parses a transcript RFC 3339 timestamp into unix
// milliseconds, falling back to now when it is missing or malformed.
func timestampMillis(timestamp string) int64 {
	if timestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
			return t.UnixMilli()
		}
	}
	return time.Now().UnixMilli()
}
