package bind

import (
	"errors"
	"strings"
	"testing"
)

// TestErrorVocabulary exercises each public bind error's Error() label + Unwrap, the three
// constructors, and Class.String — the surface integrators rely on. A copy-pasted wrong
// label or a broken Unwrap fails here (the classifier conformance test only checks types).
func TestErrorVocabulary(t *testing.T) {
	cause := errors.New("root cause")
	cases := []struct {
		err   error
		label string
	}{
		{&BindError{base{409, "m", cause}}, "bind failed"},
		{&BindAuthError{base{403, "m", cause}}, "bind rejected"},
		{&ComputerAlreadyAttachedError{base{409, "m", cause}}, "already attached"},
		{&SandboxUnusableError{base{409, "m", cause}}, "Computer sandbox cannot be attached"},
		{&SavedStateUnavailableError{base{503, "m", cause}}, "saved state unavailable"},
		{&BindTimeoutError{base{0, "m", cause}}, "timeout"},
		{&TokenRejectedError{base{401, "m", cause}}, "token rejected"},
	}
	for _, c := range cases {
		s := c.err.Error()
		if !strings.Contains(s, c.label) {
			t.Errorf("%T.Error() = %q, want it to contain %q", c.err, s, c.label)
		}
		if !strings.Contains(s, "pinesandbox:") {
			t.Errorf("%T.Error() = %q, want the pinesandbox: prefix", c.err, s)
		}
		if !errors.Is(c.err, cause) {
			t.Errorf("%T does not unwrap to its cause", c.err)
		}
	}

	// Constructors carry status + cause.
	if e := NewBindTimeoutError("x", cause); !errors.Is(e, cause) || e.Error() == "" {
		t.Error("NewBindTimeoutError broken")
	}
	if e := NewBindError(409, "x", cause); !errors.Is(e, cause) || e.Status != 409 {
		t.Error("NewBindError broken")
	}
	if e := NewTokenRejectedError(401, "x", cause); !errors.Is(e, cause) || e.Status != 401 {
		t.Error("NewTokenRejectedError broken")
	}

	// A local no-binding refusal prints its message without the "bind failed" label;
	// a status or another cause keeps the label.
	for _, c := range []struct {
		err  *BindError
		want string
	}{
		{NewBindError(0, "Computer c1: this handle has no binding (it was never attached)", ErrNotBound),
			"pinesandbox: Computer c1: this handle has no binding (it was never attached)"},
		{NewBindError(409, "m", ErrNotBound), "pinesandbox: bind failed (409): m"},
		{NewBindError(0, "m", cause), "pinesandbox: bind failed: m"},
		{NewBindError(0, "m", nil), "pinesandbox: bind failed: m"},
	} {
		if got := c.err.Error(); got != c.want {
			t.Errorf("Error() = %q, want %q", got, c.want)
		}
	}

	// Class.String.
	for cls, want := range map[Class]string{ClassReadiness: "readiness", ClassTerminal: "terminal"} {
		if cls.String() != want {
			t.Errorf("Class(%d).String() = %q, want %q", cls, cls.String(), want)
		}
	}

	// errMsg renders all four field combinations without panicking / empty.
	for _, e := range []error{
		&BindError{base{0, "", nil}}, &BindError{base{500, "", nil}}, &BindError{base{0, "msg", nil}},
	} {
		if e.Error() == "" {
			t.Errorf("%+v rendered an empty Error()", e)
		}
	}
}
