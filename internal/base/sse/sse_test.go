package sse

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// vectorFile mirrors specs/vectors/*.json (the cross-SDK SSE framing contract).
type vectorFile struct {
	Stream  string `json:"stream"`
	Vectors []struct {
		Name        string `json:"name"`
		WhatWGValid bool   `json:"whatwg_valid"`
		Wire        string `json:"wire"`
		Frames      []struct {
			Event string `json:"event"`
			Data  string `json:"data"`
			ID    string `json:"id"` // omitted in the vector ⇒ ""
		} `json:"frames"`
	} `json:"vectors"`
}

var vectorFiles = []string{"agent-events.json", "control-events.json", "exec.json"}

func collect(t *testing.T, wire string) []Frame {
	t.Helper()
	var got []Frame
	for fr, err := range Frames(strings.NewReader(wire)) {
		if err != nil {
			t.Fatalf("Frames yielded error: %v", err)
		}
		got = append(got, fr)
	}
	return got
}

// TestSSE_Vectors drives every cross-SDK vector through the framer and asserts the
// recovered frames match the contract byte-for-byte — including the today exec vectors,
// whose bare JSON must recover EMPTY-data frames (proving the framer is faithfully strict,
// not silently tolerant), and the keepalive comments, which yield nothing.
func TestSSE_Vectors(t *testing.T) {
	total := 0
	for _, name := range vectorFiles {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var vf vectorFile
		if err := json.Unmarshal(b, &vf); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		if len(vf.Vectors) == 0 {
			t.Fatalf("%s: no vectors", name)
		}
		for _, v := range vf.Vectors {
			t.Run(vf.Stream+"/"+v.Name, func(t *testing.T) {
				got := collect(t, v.Wire)
				if len(got) != len(v.Frames) {
					t.Fatalf("frame count = %d, want %d\n got %+v", len(got), len(v.Frames), got)
				}
				for i, want := range v.Frames {
					if got[i].Event != want.Event || got[i].Data != want.Data || got[i].ID != want.ID {
						t.Errorf("frame %d = {id:%q event:%q data:%q}, want {id:%q event:%q data:%q}",
							i, got[i].ID, got[i].Event, got[i].Data, want.ID, want.Event, want.Data)
					}
				}
			})
			total++
		}
	}
	if total == 0 {
		t.Fatal("no vectors exercised")
	}
}

// TestSSE_Incremental: a frame split across reads (the streaming reality) frames the same
// as the whole-buffer case — the framer must buffer across chunk boundaries.
func TestSSE_Incremental(t *testing.T) {
	// id:/event:/data: of one frame arriving in three awkward pieces.
	r := &chunkReader{chunks: []string{"id: 9\nev", "ent: status\ndata: {\"a\":1", "}\n\n"}}
	var got []Frame
	for fr, err := range Frames(r) {
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		got = append(got, fr)
	}
	if len(got) != 1 || got[0].ID != "9" || got[0].Event != "status" || got[0].Data != `{"a":1}` {
		t.Fatalf("incremental framing = %+v", got)
	}
}

// TestSSE_TrailingUnterminatedDiscarded: a block with no closing blank line is dropped
// (truncation safety) — matches the reference decoder, NOT a lenient flush.
func TestSSE_TrailingUnterminatedDiscarded(t *testing.T) {
	got := collect(t, "data: {\"a\":1}\n\ndata: {\"b\":2}\n") // 2nd block unterminated
	if len(got) != 1 || got[0].Data != `{"a":1}` {
		t.Fatalf("want only the terminated frame, got %+v", got)
	}
}

// chunkReader hands out its chunks one Read at a time, to exercise cross-chunk framing.
type chunkReader struct{ chunks []string }

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	c.chunks[0] = c.chunks[0][n:]
	if c.chunks[0] == "" {
		c.chunks = c.chunks[1:]
	}
	return n, nil
}

// TestSSE_VectorsMatchCanonical guards the module-local vector copies against the
// canonical specs/vectors (skips on the mirror — §9.1).
func TestSSE_VectorsMatchCanonical(t *testing.T) {
	for _, name := range vectorFiles {
		canonical := filepath.Join("..", "..", "..", "..", "..", "..", "specs", "vectors", name)
		want, err := os.ReadFile(canonical)
		if err != nil {
			t.Skipf("canonical vectors not present (mirror build): %v", err)
		}
		got, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("read testdata %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Fatalf("testdata/%s drifted from %s — re-copy the canonical vector", name, canonical)
		}
	}
}
