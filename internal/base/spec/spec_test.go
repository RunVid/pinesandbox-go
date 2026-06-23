package spec

import (
	"errors"
	"testing"
)

func neg() Negotiator {
	return Negotiator{RequestHeader: "Computer-Spec-Version", ResponseHeader: "X-Computer-Spec-Version", SupportedMajor: 1}
}

func TestRequestValue(t *testing.T) {
	if got := neg().RequestValue(); got != "1" {
		t.Errorf("RequestValue() = %q, want \"1\"", got)
	}
	if got := (Negotiator{SupportedMajor: 12}).RequestValue(); got != "12" {
		t.Errorf("RequestValue() = %q, want \"12\"", got)
	}
}

// TestCheck mirrors the Ruby SpecVersionMiddleware#on_complete contract: absent/blank echo
// tolerated, same major tolerated (incl. minor/suffix), different major rejected.
func TestCheck(t *testing.T) {
	n := neg()
	cases := []struct {
		name      string
		echoed    string
		wantMatch bool // true = no error (tolerated/match)
	}{
		{"absent", "", true},
		{"blank", "   ", true},
		{"exact major", "1", true},
		{"major with minor", "1.4", true},
		{"major with suffix", "1-rc2", true},
		{"padded", "  1  ", true},
		{"different major", "2", false},
		{"different major with minor", "2.0", false},
		{"non-numeric is major 0", "abc", false}, // Ruby "abc".to_i == 0 != 1 → mismatch
		{"zero", "0", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := n.Check(c.echoed)
			if c.wantMatch && err != nil {
				t.Errorf("Check(%q) = %v, want nil", c.echoed, err)
			}
			if !c.wantMatch {
				if err == nil {
					t.Fatalf("Check(%q) = nil, want MismatchError", c.echoed)
				}
				var me *MismatchError
				if !errors.As(err, &me) {
					t.Fatalf("Check(%q) = %T, want *MismatchError", c.echoed, err)
				}
				if me.Supported != 1 {
					t.Errorf("MismatchError.Supported = %d, want 1", me.Supported)
				}
			}
		})
	}
}

// TestCheck_TrimsServed records the trimmed (not raw) echo so the surfaced error and the
// caller see the same value.
func TestCheck_ServedIsTrimmed(t *testing.T) {
	var me *MismatchError
	if !errors.As(neg().Check("  2.0  "), &me) {
		t.Fatal("expected MismatchError")
	}
	if me.Served != "2.0" {
		t.Errorf("Served = %q, want \"2.0\"", me.Served)
	}
}

func TestLeadingInt(t *testing.T) {
	cases := map[string]int{
		"1": 1, "12": 12, "1.4": 1, "2.0": 2, "1-rc": 1,
		"": 0, "abc": 0, "x9": 0, "-3": -3, "+5": 5, "007": 7, "9z9": 9,
	}
	for in, want := range cases {
		if got := leadingInt(in); got != want {
			t.Errorf("leadingInt(%q) = %d, want %d", in, got, want)
		}
	}
}
