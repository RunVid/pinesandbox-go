package sse

import (
	"strings"
	"testing"
)

// TestFrames_RejectsOversizeLine: a single line past the cap (no newline) errors instead of
// buffering unboundedly.
func TestFrames_RejectsOversizeLine(t *testing.T) {
	huge := "data: " + strings.Repeat("x", 200) // > the 64-byte test cap, no trailing \n\n
	var gotErr error
	for _, err := range framesWithCap(strings.NewReader(huge), 64) {
		if err != nil {
			gotErr = err
		}
	}
	if gotErr == nil {
		t.Fatal("expected an error for a line exceeding the cap")
	}
}

// TestFrames_RejectsOversizeFrame: many small data: lines that together exceed the cap error
// before the frame is dispatched.
func TestFrames_RejectsOversizeFrame(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("data: chunk\n") // ~12 bytes each → ~600 > 64 cap, no blank line yet
	}
	var gotErr error
	for _, err := range framesWithCap(strings.NewReader(sb.String()), 64) {
		if err != nil {
			gotErr = err
		}
	}
	if gotErr == nil {
		t.Fatal("expected an error for accumulated frame bytes exceeding the cap")
	}
}

// TestFrames_UnderCapStillWorks: a normal frame under the cap parses fine.
func TestFrames_UnderCapStillWorks(t *testing.T) {
	var got string
	for f, err := range framesWithCap(strings.NewReader("data: hi\n\n"), 64) {
		if err != nil {
			t.Fatal(err)
		}
		got = f.Data
	}
	if got != "hi" {
		t.Errorf("Data = %q, want hi", got)
	}
}
