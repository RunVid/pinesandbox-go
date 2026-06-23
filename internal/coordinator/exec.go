package coordinator

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"go.pinesandbox.io/computer/internal/base/sse"
)

// ExecOptions are the optional knobs for Exec.
type ExecOptions struct {
	Cwd       string
	TimeoutMs *int
}

// ExecResult is the assembled outcome of a streamed command: stdout/stderr accumulated from
// their events (each line re-terminated with "\n", matching the Ruby adapter so callers can
// iterate lines), the exit code (nil if the stream never reported one), and an error message
// from an error event.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode *int
	Error    string
}

// Exec runs command in the session's bash terminal over an SSE stream (POST
// sessions/{name}/exec). fn (optional) receives each parsed event. A non-2xx (e.g. 409
// terminal_lost) is a problem+json body, surfaced as *problem.APIError — recreate the
// terminal and retry. Mirrors the Ruby adapter's exec accumulation.
func (c *Client) Exec(ctx context.Context, token, name, command string, opts ExecOptions, fn func(event map[string]any) error) (*ExecResult, error) {
	body, err := json.Marshal(execBody(command, opts))
	if err != nil {
		return nil, err
	}
	sr, err := c.openSSE(ctx, "POST", "/sessions/"+url.PathEscape(name)+"/exec", token, body, nil)
	if err != nil {
		return nil, err
	}
	defer sr.Body.Close()

	res := &ExecResult{}
	for frame, ferr := range sse.Frames(sr.Body) {
		if ferr != nil {
			return res, ferr
		}
		if frame.Data == "" {
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(frame.Data), &ev) != nil {
			continue // tolerate a non-JSON frame (e.g. a stray keepalive)
		}
		accumulate(res, ev)
		if fn != nil {
			if err := fn(ev); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

func execBody(command string, opts ExecOptions) map[string]any {
	body := map[string]any{"command": command}
	if opts.Cwd != "" {
		body["cwd"] = opts.Cwd
	}
	if opts.TimeoutMs != nil {
		body["timeout_ms"] = *opts.TimeoutMs
	}
	return body
}

// accumulate folds one exec event into the result. The real execd/coord wire (verified
// against a live coord): stdout/stderr carry `text`; a clean finish is an
// `execution_complete` event with NO exit code (success ⇒ 0); a non-zero exit is an `error`
// event whose `error` object carries the code in `evalue` (a stringified int) plus a
// `traceback`. `init`/`ping` events are ignored.
func accumulate(res *ExecResult, ev map[string]any) {
	text, _ := ev["text"].(string)
	switch ev["type"] {
	case "stdout":
		res.Stdout += text + "\n"
	case "stderr":
		res.Stderr += text + "\n"
	case "error":
		if errObj, ok := ev["error"].(map[string]any); ok {
			if code, ok := evalueCode(errObj); ok {
				res.ExitCode = &code // the error event is authoritative for the exit code
			}
			res.Error = errMessage(errObj)
		} else if s, ok := ev["error"].(string); ok && s != "" {
			res.Error = s
		} else if text != "" {
			res.Error = text
		}
	case "execution_complete":
		if res.ExitCode == nil { // a clean complete with no prior error ⇒ exit 0
			zero := 0
			res.ExitCode = &zero
		}
	}
	// Defensive: honor a top-level exit_code if a future wire adds one (no-op on today's).
	if res.ExitCode == nil {
		if v, ok := ev["exit_code"].(float64); ok {
			code := int(v)
			res.ExitCode = &code
		}
	}
}

// evalueCode parses the exit code from an error object's `evalue` (a stringified int).
func evalueCode(errObj map[string]any) (int, bool) {
	if v, ok := errObj["evalue"].(string); ok {
		if code, err := strconv.Atoi(v); err == nil {
			return code, true
		}
	}
	return 0, false
}

// errMessage renders the error object's traceback (preferred), else its name/value.
func errMessage(errObj map[string]any) string {
	if tb, ok := errObj["traceback"].([]any); ok {
		var parts []string
		for _, t := range tb {
			if s, ok := t.(string); ok {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	name, _ := errObj["ename"].(string)
	val, _ := errObj["evalue"].(string)
	switch {
	case name != "" && val != "":
		return name + ": " + val
	case name != "":
		return name
	default:
		return ""
	}
}
