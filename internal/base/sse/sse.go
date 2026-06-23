// Package sse is the strict SSE framing layer for the Computer's event streams (agent,
// author, control events). It does FRAMING ONLY — split a byte stream into blank-line-
// terminated frames and yield Frame{ID, Event, Data} each — leaving each stream to layer
// its own decoder (control → typed structs, agent/author → JSON). Generic base primitive
// (internal/base) — Computer-agnostic, must not import a domain package (§3).
//
// The framing is pinned by the cross-SDK vectors (specs/vectors), validated identically
// by check-vectors.py and the web SDK. NOTE the contract subtleties (both load-bearing):
//   - a block dispatches a frame when ANY field line was seen — even an unknown one — so
//     data may be empty (this is why today's prefix-less exec wire, whose JSON `"type":`
//     parses as an unknown field, yields empty-data frames: the migration proof);
//   - an unterminated trailing block (no closing blank line) is DISCARDED, not flushed
//     (exec truncation safety).
//
// exec's bare-JSON tolerance is NOT here — it lives in the exec decoder (§8), so this
// stays a faithful SSE framer.
package sse

import (
	"bufio"
	"fmt"
	"io"
	"iter"
	"strings"
)

// maxFrameBytes caps a single line and the accumulated bytes of one frame, so a buggy or
// hostile server can't OOM a server-side SDK with an unterminated giant line or an unbounded
// run of data: lines. Generous for any legitimate event (incl. inline images); fatal for a
// multi-megabyte+ pathological frame.
const maxFrameBytes = 16 << 20 // 16 MiB

// Frame is one decoded SSE frame. Event defaults to "message"; Data is the data: lines
// joined with "\n"; ID is the block's id: value ("" if none — per-block, not sticky; the
// resume layer tracks the last non-empty id).
type Frame struct {
	ID    string
	Event string
	Data  string
}

// Frames returns an iterator over the SSE frames in r (range-over-func, §8). On a read
// error the final iteration yields a zero Frame and the error.
func Frames(r io.Reader) iter.Seq2[Frame, error] {
	return framesWithCap(r, maxFrameBytes)
}

// framesWithCap is Frames with an injectable size cap (tests use a small one).
func framesWithCap(r io.Reader, maxFrameBytes int) iter.Seq2[Frame, error] {
	return func(yield func(Frame, error) bool) {
		br := bufio.NewReader(r)
		event := "message"
		var dataLines []string
		id := ""
		sawField := false
		frameBytes := 0
		reset := func() {
			event, dataLines, id, sawField, frameBytes = "message", nil, "", false, 0
		}

		for {
			line, err := readLine(br, maxFrameBytes)
			eof := err == io.EOF
			if err != nil && !eof {
				yield(Frame{}, err)
				return
			}
			frameBytes += len(line)
			if frameBytes > maxFrameBytes {
				yield(Frame{}, fmt.Errorf("sse: frame exceeds the %d-byte cap", maxFrameBytes))
				return
			}

			// Strip the line terminator (\n and any preceding \r).
			s := strings.TrimSuffix(line, "\n")
			s = strings.TrimSuffix(s, "\r")

			if s == "" {
				if eof {
					return // unterminated trailing block (if any) is discarded
				}
				if sawField {
					f := Frame{ID: id, Event: event, Data: strings.Join(dataLines, "\n")}
					if !yield(f, nil) {
						return
					}
					reset()
				}
				continue
			}

			if !strings.HasPrefix(s, ":") { // ":" → comment, ignored (does NOT set sawField)
				field, value := s, ""
				if i := strings.IndexByte(s, ':'); i >= 0 {
					field, value = s[:i], s[i+1:]
					value = strings.TrimPrefix(value, " ") // strip exactly one leading space
				}
				sawField = true
				switch field {
				case "event":
					event = value
				case "data":
					dataLines = append(dataLines, value)
				case "id":
					id = value
					// "retry" + unknown fields ignored (but still marked sawField above)
				}
			}

			if eof {
				return // final non-terminated line → block unterminated → discard
			}
		}
	}
}

// readLine reads through the next '\n' (inclusive), or returns an error once it has buffered
// more than max bytes without one — bounding a single unterminated line. It reads through
// the bufio buffer (no per-byte syscall), so the byte-at-a-time loop is cheap.
func readLine(br *bufio.Reader, max int) (string, error) {
	var sb strings.Builder
	for {
		b, err := br.ReadByte()
		if err != nil {
			return sb.String(), err
		}
		sb.WriteByte(b)
		if b == '\n' {
			return sb.String(), nil
		}
		if sb.Len() > max {
			return sb.String(), fmt.Errorf("sse: line exceeds the %d-byte cap", max)
		}
	}
}
