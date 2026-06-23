package controlplane

import "testing"

func TestNormalizeStatus(t *testing.T) {
	cases := map[string]string{
		"Running":        "running",
		"creatingPod":    "creating_pod",
		"PausedByUser":   "paused_by_user",
		"RUNNING":        "running",
		"paused":         "paused",
		"":               "",
		"state2Done":     "state2_done", // digit→Upper boundary
		"HTTPError":      "httperror",   // no lower/digit before an upper run
		"resumeRequired": "resume_required",
	}
	for in, want := range cases {
		if got := normalizeStatus(in); got != want {
			t.Errorf("normalizeStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSandboxInfo_MinimalAndMissing(t *testing.T) {
	// Create responses omit image; metadata defaults to a non-nil map; bad times → nil.
	info, err := ParseSandboxInfo([]byte(`{"id":"x","status":{"state":"creatingPod"},"createdAt":"bogus"}`))
	if err != nil {
		t.Fatalf("ParseSandboxInfo: %v", err)
	}
	if info.ID != "x" || info.Status != "creating_pod" {
		t.Errorf("info = %+v", info)
	}
	if info.Image != "" {
		t.Errorf("Image = %q, want empty (omitted in create)", info.Image)
	}
	if info.Metadata == nil {
		t.Error("Metadata is nil, want empty map")
	}
	if info.CreatedAt != nil {
		t.Errorf("CreatedAt = %v, want nil for an unparseable time", info.CreatedAt)
	}
}

func TestParseSandboxInfo_Invalid(t *testing.T) {
	if _, err := ParseSandboxInfo([]byte(`not json`)); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
