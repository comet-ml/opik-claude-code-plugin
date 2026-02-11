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
	rand.Read(randBytes)
	
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

// toV7 converts any UUID to a deterministic UUIDv7 using MD5 hash
// This enables idempotent upserts by generating consistent IDs from transcript UUIDs
func toV7(uuid string) string {
	hash := md5.Sum([]byte(uuid))
	h := hex.EncodeToString(hash[:])
	
	// Set version to 7 (0111) in byte 6
	b6 := (hash[6] & 0x0F) | 0x70
	b6Hex := fmt.Sprintf("%02x", b6)
	
	// Set variant to 10 in byte 8
	b8 := (hash[8] & 0x3F) | 0x80
	b8Hex := fmt.Sprintf("%02x", b8)
	
	return fmt.Sprintf("%s-%s-%s%s-%s%s-%s",
		h[0:8],
		h[8:12],
		b6Hex,
		h[14:16],
		b8Hex,
		h[18:20],
		h[20:32])
}
