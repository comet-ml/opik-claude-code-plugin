package main

import (
	"testing"
	"time"
)

func TestAPITimeouts(t *testing.T) {
	if got := NewAPI(&Config{}).client.Timeout; got != 2*time.Second {
		t.Errorf("foreground timeout = %s, want 2s", got)
	}
	if got := NewAPIWithTimeout(&Config{}, backgroundAPITimeout).client.Timeout; got != 30*time.Second {
		t.Errorf("background timeout = %s, want 30s", got)
	}
}
