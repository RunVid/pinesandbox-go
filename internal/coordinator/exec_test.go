package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"go.pinesandbox.io/computer/internal/base/problem"
)

func TestExec_AccumulatesStream(t *testing.T) {
	var gotBody map[string]any
	var events int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/s/exec" || r.Method != "POST" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		// Real SSE data: frames (coord re-frames execd events). init/ping are ignored; a
		// clean finish is execution_complete with NO exit code (success ⇒ 0).
		_, _ = io.WriteString(w, "data: {\"type\":\"init\",\"text\":\"sess-1\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"stdout\",\"text\":\"hello\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"stderr\",\"text\":\"warn\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"execution_complete\",\"execution_time\":32}\n\n")
	})

	tms := 5000
	res, err := c.Exec(context.Background(), "ps_", "s", "echo hello", ExecOptions{Cwd: "/tmp", TimeoutMs: &tms}, func(map[string]any) error {
		events++
		return nil
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.Stdout != "hello\n" || res.Stderr != "warn\n" {
		t.Errorf("stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Errorf("exit code = %v, want 0", res.ExitCode)
	}
	if events != 4 {
		t.Errorf("callback ran %d times, want 4 (init+stdout+stderr+complete)", events)
	}
	if gotBody["command"] != "echo hello" || gotBody["cwd"] != "/tmp" || gotBody["timeout_ms"].(float64) != 5000 {
		t.Errorf("body = %v", gotBody)
	}
}

// TestExec_NonzeroExit: a non-zero exit arrives as an error event whose error.evalue is the
// code (the real execd/coord wire — no execution_complete follows).
func TestExec_NonzeroExit(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "data: {\"type\":\"stdout\",\"text\":\"oops\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"ename\":\"CommandExecError\",\"evalue\":\"3\",\"traceback\":[\"exit status 3\"]}}\n\n")
	})
	res, err := c.Exec(context.Background(), "ps_", "s", "false", ExecOptions{}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode == nil || *res.ExitCode != 3 {
		t.Errorf("exit code = %v, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Error, "exit status 3") {
		t.Errorf("Error = %q, want it to include the traceback", res.Error)
	}
	if res.Stdout != "oops\n" {
		t.Errorf("stdout = %q", res.Stdout)
	}
}

// TestExec_CallbackAbort: a callback returning an error stops the stream and is returned.
func TestExec_CallbackAbort(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "data: {\"type\":\"stdout\",\"text\":\"a\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"stdout\",\"text\":\"b\"}\n\n")
	})
	stop := errors.New("stop")
	n := 0
	_, err := c.Exec(context.Background(), "ps_", "s", "x", ExecOptions{}, func(map[string]any) error {
		n++
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want the callback error", err)
	}
	if n != 1 {
		t.Errorf("callback ran %d times, want 1 (aborted after first)", n)
	}
}

func TestExec_TerminalLostIsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(409)
		_, _ = io.WriteString(w, `{"type":"/errors/terminal-lost","status":409,"detail":"recreate the terminal"}`)
	})
	var ae *problem.APIError
	if _, err := c.Exec(context.Background(), "ps_", "s", "ls", ExecOptions{}, nil); !errors.As(err, &ae) {
		t.Fatalf("err = %T (%v), want *problem.APIError", err, err)
	} else if ae.Status != 409 {
		t.Errorf("status = %d, want 409", ae.Status)
	}
}
