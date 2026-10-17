package pinesandbox

// The runner for contract/scenarios/*.json: deterministic failure sequences every server
// SDK must handle the same way. Each scenario builds a Client against a scripted HTTP
// transport keyed by host and a virtual clock, performs one facade call, and checks every
// request and the outcome by the rules in contract/scenarios/README.md.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
)

const (
	scenarioEndpoint = "https://scenario.pinesandbox.io"
	scenarioAPIKey   = "pk_scenario"
	scenarioControl  = "api.scenario.pinesandbox.io"
)

type scenario struct {
	Name  string `json:"name"`
	Given struct {
		Computer *struct {
			ID            string `json:"id"`
			Key           string `json:"key"`
			RuntimeID     string `json:"runtime_id"`
			ComputerToken string `json:"computer_token"`
		} `json:"computer"`
		Session *struct {
			Name  string `json:"name"`
			Token string `json:"token"`
		} `json:"session"`
		Credentials json.RawMessage `json:"credentials"`
	} `json:"given"`
	Call struct {
		Verb string         `json:"verb"`
		Args map[string]any `json:"args"`
	} `json:"call"`
	Exchanges []scenarioExchange         `json:"exchanges"`
	Expect    map[string]json.RawMessage `json:"expect"`
	SleepsMS  *[]int64                   `json:"sleeps_ms"`
}

type scenarioExchange struct {
	Request struct {
		Method  string            `json:"method"`
		Host    string            `json:"host"`
		Path    string            `json:"path"`
		Auth    string            `json:"auth"`
		Query   map[string]string `json:"query"`
		Headers map[string]string `json:"headers"`
		JSON    json.RawMessage   `json:"json"`
	} `json:"request"`
	Response struct {
		Status         int               `json:"status"`
		Headers        map[string]string `json:"headers"`
		JSON           json.RawMessage   `json:"json"`
		Text           *string           `json:"text"`
		SSE            *string           `json:"sse"`
		TransportError bool              `json:"transport_error"`
	} `json:"response"`
}

type scenarioError struct {
	Class  string         `json:"class"`
	Fields map[string]any `json:"fields"`
	Cause  string         `json:"cause"`
}

// scenarioErrorClasses maps the contract's error class names to Go types. A class
// matches only the returned error itself, never an error it wraps, as the other runners
// compare the thrown class.
var scenarioErrorClasses = map[string]func(error) bool{
	"ApiError":                          isErrorType[*APIError],
	"ConnectionError":                   isErrorType[*ConnectionError],
	"TimeoutError":                      isErrorType[*TimeoutError],
	"TokenRejectedError":                isErrorType[*TokenRejectedError],
	"RuntimeGoneError":                  isErrorType[*RuntimeGoneError],
	"RunUnconfirmedError":               isErrorType[*RunUnconfirmedError],
	"CapacityExceededError":             isErrorType[*CapacityExceededError],
	"BindingRevisionConflictError":      isErrorType[*BindingRevisionConflictError],
	"AttachAuthorizationCommittedError": isErrorType[*AttachAuthorizationCommittedError],
	"RuntimeUnusableError":              isErrorType[*RuntimeUnusableError],
	"RateLimitedError":                  isErrorType[*RateLimited],
	// Go's stream iterators yield the ErrStreamLost sentinel, wrapping the last fault.
	"StreamLostError": func(err error) bool { return errors.Is(err, ErrStreamLost) },
}

func isErrorType[T error](err error) bool {
	_, ok := err.(T)
	return ok
}

func TestContractScenarios(t *testing.T) {
	dir := filepath.Join("..", "contract", "scenarios")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Skipf("contract scenarios not present (mirror build): %v", err)
	}
	sort.Strings(files)
	total := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Scenarios []scenario `json:"scenarios"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, s := range f.Scenarios {
			total++
			t.Run(filepath.Base(file)+"/"+s.Name, func(t *testing.T) { runScenario(t, s) })
		}
	}
	if total == 0 {
		t.Fatal("no scenarios ran")
	}
}

func runScenario(t *testing.T, s scenario) {
	clock := &virtualClock{now: time.UnixMilli(1_760_000_000_000)}
	runtimeID := "rt-1"
	if s.Given.Computer != nil {
		runtimeID = s.Given.Computer.RuntimeID
	}
	script := &scriptedTransport{
		t:           t,
		hosts:       map[string]string{"control": scenarioControl, "computer": runtimeID + ".computer.scenario.pinesandbox.io"},
		pending:     append([]scenarioExchange(nil), s.Exchanges...),
		seenHeaders: map[string]string{},
		seenJSON:    map[string]any{},
	}
	client, err := newClient(ClientOptions{Endpoint: scenarioEndpoint, APIKey: scenarioAPIKey},
		clientHooks{httpClient: &http.Client{Transport: script}, clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var computer *Computer
	var session *Session
	if g := s.Given.Computer; g != nil {
		key, err := base64.StdEncoding.DecodeString(g.Key)
		if err != nil {
			t.Fatal(err)
		}
		if computer, err = client.AdoptExisting(ctx, g.ID, key, g.RuntimeID, g.ComputerToken); err != nil {
			t.Fatal(err)
		}
		if g := s.Given.Session; g != nil {
			if session, err = computer.AdoptSession(g.Name, g.Token); err != nil {
				t.Fatal(err)
			}
		}
	}
	var credentials Credentials
	if len(s.Given.Credentials) > 0 {
		if err := json.Unmarshal(s.Given.Credentials, &credentials); err != nil {
			t.Fatalf("given.credentials: %v", err)
		}
	}

	value, events, callErr := callScenario(ctx, t, s, client, computer, session, credentials)

	if raw, ok := s.Expect["error"]; ok {
		var want scenarioError
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		checkScenarioError(t, callErr, want)
	} else {
		if callErr != nil {
			t.Fatalf("call failed: %T %v", callErr, callErr)
		}
		if raw, ok := s.Expect["events"]; ok {
			var want any
			_ = json.Unmarshal(raw, &want)
			got := plain(events)
			if msg := matchSubset(got, want, "events"); msg != "" {
				t.Errorf("events: %s\n got %s", msg, mustJSON(got))
			}
		}
		if raw, ok := s.Expect["result"]; ok {
			var want any
			_ = json.Unmarshal(raw, &want)
			got := plain(value)
			if _, object := want.(map[string]any); object || isSlice(want) {
				if msg := matchSubset(got, want, "result"); msg != "" {
					t.Errorf("result: %s\n got %s", msg, mustJSON(got))
				}
			} else if !reflect.DeepEqual(got, want) {
				t.Errorf("result = %s, want %s", mustJSON(got), raw)
			}
		}
	}
	script.mu.Lock()
	for _, p := range script.pending {
		t.Errorf("expected request never sent: %s %s", p.Request.Method, p.Request.Path)
	}
	script.mu.Unlock()
	if s.SleepsMS != nil {
		if got := clock.recorded(); !reflect.DeepEqual(got, *s.SleepsMS) && !(len(got) == 0 && len(*s.SleepsMS) == 0) {
			t.Errorf("sleeps = %v ms, want %v", got, *s.SleepsMS)
		}
	}
}

// callScenario maps a canonical verb and its snake_case args onto the Go facade.
func callScenario(ctx context.Context, t *testing.T, s scenario, client *Client, computer *Computer, session *Session, credentials Credentials) (any, []any, error) {
	t.Helper()
	str := func(k string) string {
		v, _ := s.Call.Args[k].(string)
		return v
	}
	stopAfter := -1
	if raw, ok := s.Expect["stop_after"]; ok {
		_ = json.Unmarshal(raw, &stopAfter)
	}
	needSession := func() *Session {
		if session == nil {
			t.Fatalf("%s needs given.session", s.Call.Verb)
		}
		return session
	}
	needComputer := func() *Computer {
		if computer == nil {
			t.Fatalf("%s needs given.computer", s.Call.Verb)
		}
		return computer
	}
	switch s.Call.Verb {
	case "AgentMode.run":
		return value(needSession().Agent().Run(ctx, str("goal"), RunOptions{IdempotencyKey: str("idempotency_key")}))
	case "AgentMode.answer":
		return value(needSession().Agent().Answer(ctx, str("turn_id"), str("answer"), AnswerOptions{}))
	case "AgentMode.steer":
		return value(needSession().Agent().Steer(ctx, str("turn_id"), str("text"), SteerOptions{}))
	case "AgentMode.cancel":
		return value(needSession().Agent().Cancel(ctx, str("turn_id")))
	case "AgentMode.wait":
		ms, _ := s.Call.Args["timeout_ms"].(float64)
		return value(needSession().Agent().Wait(ctx, str("turn_id"), time.Duration(ms)*time.Millisecond))
	case "AgentMode.events":
		return collect(needSession().Agent().Events(ctx, str("last_event_id"), EventsOptions{}), stopAfter)
	case "Session.controlEvents":
		return collect(needSession().ControlEvents(ctx, str("last_event_id")), stopAfter)
	case "Session.authorEvents":
		events := []any{}
		_, err := needSession().AuthorEvents(ctx, str("author_id"), str("last_event_id"), func(data []byte) error {
			var ev any
			if err := json.Unmarshal(data, &ev); err != nil {
				return err
			}
			events = append(events, ev)
			if stopAfter >= 0 && len(events) >= stopAfter {
				return ErrStop
			}
			return nil
		})
		if err != nil && !errors.Is(err, ErrStop) {
			return nil, nil, err
		}
		return nil, events, nil
	case "Session.exec":
		return value(needSession().Exec(ctx, str("command"), ExecOptions{}, nil))
	case "Session.listTabs":
		return value(needSession().ListTabs(ctx))
	case "Session.createTab":
		return value(needSession().CreateTab(ctx, str("url"), ""))
	case "Session.takeControl":
		return value(needSession().TakeControl(ctx))
	case "Session.notifyHuman":
		return nil, nil, needSession().NotifyHuman(ctx, str("reason"), "", "")
	case "Computer.status":
		return value(needComputer().Status(ctx))
	case "Computer.stop":
		return value(needComputer().Stop(ctx))
	case "Computer.sessions":
		return value(needComputer().Sessions(ctx))
	case "Client.stopComputer":
		return value(client.StopComputer(ctx, str("id")))
	case "Client.createComputer":
		c, err := client.CreateComputer(ctx, AttachOptions{Credentials: credentials})
		if err != nil {
			return nil, nil, err
		}
		return map[string]any{
			"id": c.ID(), "runtime_id": c.RuntimeID(), "binding_revision": c.BindingRevision(),
			"computer_token": c.ComputerToken(), "persistence_mode": c.PersistenceMode(),
		}, nil, nil
	default:
		t.Fatalf("the runner has no Go mapping for %s", s.Call.Verb)
		return nil, nil, nil
	}
}

func value[T any](v T, err error) (any, []any, error) {
	if err != nil {
		return nil, nil, err
	}
	return v, nil, nil
}

func collect[T any](seq iter.Seq2[T, error], stopAfter int) (any, []any, error) {
	events := []any{}
	for ev, err := range seq {
		if err != nil {
			return nil, nil, err
		}
		events = append(events, ev)
		if stopAfter >= 0 && len(events) >= stopAfter {
			break
		}
	}
	return nil, events, nil
}

func checkScenarioError(t *testing.T, err error, want scenarioError) {
	t.Helper()
	if err == nil {
		t.Fatalf("call succeeded, want %s", want.Class)
	}
	matches, known := scenarioErrorClasses[want.Class]
	if !known {
		t.Fatalf("the runner has no Go type for error class %s", want.Class)
	}
	if !matches(err) {
		t.Fatalf("error = %T (%v), want %s", err, err, want.Class)
	}
	fields, _ := plain(err).(map[string]any)
	for name, v := range want.Fields {
		if got := fields[name]; !reflect.DeepEqual(got, v) {
			t.Errorf("%s.%s = %v, want %v", want.Class, name, mustJSON(got), mustJSON(v))
		}
	}
	if want.Cause != "" {
		cause := errors.Unwrap(err)
		causeMatches, known := scenarioErrorClasses[want.Cause]
		if !known {
			t.Fatalf("the runner has no Go type for cause class %s", want.Cause)
		}
		if cause == nil || !causeMatches(cause) {
			t.Errorf("cause = %T (%v), want %s", cause, cause, want.Cause)
		}
	}
}

// ---- the virtual clock ----

// virtualClock returns from every sleep at once, advancing its time and recording the
// wait; jitter is 0.
type virtualClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []int64
}

func (c *virtualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *virtualClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, int64(math.Round(float64(d)/float64(time.Millisecond))))
	c.now = c.now.Add(d)
	return nil
}

func (c *virtualClock) Jitter() float64 { return 0 }

func (c *virtualClock) recorded() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.sleeps...)
}

// ---- the scripted transport ----

// scriptedTransport answers each request with the next scripted exchange after checking
// it against the scenario's expected request.
type scriptedTransport struct {
	t           *testing.T
	hosts       map[string]string
	mu          sync.Mutex
	pending     []scenarioExchange
	seenHeaders map[string]string // lower-cased name → the last request's value
	seenJSON    map[string]any    // path → the last JSON body sent to it
}

func (s *scriptedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	label := req.Method + " " + req.URL.Host + req.URL.EscapedPath()
	if len(s.pending) == 0 {
		s.t.Errorf("unexpected request %s", label)
		return nil, fmt.Errorf("scenario: unexpected request %s", label)
	}
	next := s.pending[0]
	s.pending = s.pending[1:]
	want := next.Request
	if wantLabel := want.Method + " " + s.hosts[want.Host] + want.Path; label != wantLabel {
		s.t.Errorf("request = %s, want %s", label, wantLabel)
	}
	for k, v := range want.Query {
		if got := req.URL.Query().Get(k); got != v {
			s.t.Errorf("%s query %s = %q, want %q", label, k, got, v)
		}
	}
	if got := authClass(req.Header.Get("Authorization")); got != want.Auth {
		s.t.Errorf("%s auth = %s, want %s", label, got, want.Auth)
	}
	for name, v := range want.Headers {
		got, present := req.Header.Get(name), len(req.Header.Values(name)) > 0
		previous := s.seenHeaders[strings.ToLower(name)]
		switch v {
		case "$absent":
			if present {
				s.t.Errorf("%s header %s = %q, want it absent", label, name, got)
			}
		case "$same":
			if got != previous {
				s.t.Errorf("%s header %s = %q, want the previous %q", label, name, got, previous)
			}
		case "$fresh":
			if !present || got == "" || got == previous {
				s.t.Errorf("%s header %s = %q, want a value other than %q", label, name, got, previous)
			}
		default:
			if got != v {
				s.t.Errorf("%s header %s = %q, want %q", label, name, got, v)
			}
		}
	}
	for name, values := range req.Header {
		s.seenHeaders[strings.ToLower(name)] = values[len(values)-1]
	}
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	if len(want.JSON) > 0 {
		var got, wantJSON any
		if err := json.Unmarshal(body, &got); err != nil {
			s.t.Errorf("%s body is not JSON: %q", label, body)
		}
		_ = json.Unmarshal(want.JSON, &wantJSON)
		if msg := matchRequestJSON(got, wantJSON, s.seenJSON[want.Path], "json"); msg != "" {
			s.t.Errorf("%s %s\n body %s", label, msg, body)
		}
		s.seenJSON[want.Path] = got
	}

	resp := next.Response
	if resp.TransportError {
		return nil, errors.New("scenario: connection reset by peer")
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	header := http.Header{}
	for k, v := range resp.Headers {
		header.Set(k, v)
	}
	var out string
	switch {
	case len(resp.JSON) > 0:
		out = string(resp.JSON)
		if header.Get("Content-Type") == "" {
			var obj map[string]json.RawMessage
			if json.Unmarshal(resp.JSON, &obj) == nil && obj["type"] != nil {
				header.Set("Content-Type", "application/problem+json")
			} else {
				header.Set("Content-Type", "application/json")
			}
		}
	case resp.SSE != nil:
		out = *resp.SSE
		header.Set("Content-Type", "text/event-stream")
	case resp.Text != nil && *resp.Text != "":
		out = *resp.Text
		header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	if status == http.StatusNoContent || status == http.StatusNotModified {
		out = ""
	}
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode: status, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: header, Body: io.NopCloser(strings.NewReader(out)), ContentLength: int64(len(out)), Request: req,
	}, nil
}

// authClass names the credential a request carried, as the scenarios spell it.
func authClass(header string) string {
	token, ok := strings.CutPrefix(header, "Bearer ")
	switch {
	case !ok || token == "":
		return "none"
	case strings.HasPrefix(token, "ct_"):
		return "computer"
	case strings.HasPrefix(token, "ps_"):
		return "session"
	case strings.HasPrefix(token, "pk_"):
		return "project_key"
	case len(strings.Split(token, ".")) == 3:
		return "project_token"
	default:
		return "unknown"
	}
}

// matchRequestJSON is the README's request-body rule: a deep subset of the body, where
// "$any" requires the key present, "$absent" requires it absent, and "$same" requires the
// value the previous request to the same path sent.
func matchRequestJSON(got, want, previous any, at string) string {
	wantObj, ok := want.(map[string]any)
	if !ok {
		if !reflect.DeepEqual(got, want) {
			return fmt.Sprintf("%s = %s, want %s", at, mustJSON(got), mustJSON(want))
		}
		return ""
	}
	gotObj, ok := got.(map[string]any)
	if !ok {
		return fmt.Sprintf("%s = %s, want an object", at, mustJSON(got))
	}
	prevObj, _ := previous.(map[string]any)
	for k, w := range wantObj {
		g, present := gotObj[k]
		path := at + "." + k
		switch w {
		case "$absent":
			if present {
				return fmt.Sprintf("%s = %s, want it absent", path, mustJSON(g))
			}
		case "$any":
			if !present {
				return path + " is absent, want it present"
			}
		case "$same":
			if p := prevObj[k]; !present || !reflect.DeepEqual(g, p) {
				return fmt.Sprintf("%s = %s, want the previous %s", path, mustJSON(g), mustJSON(p))
			}
		default:
			if !present {
				return path + " is absent"
			}
			if msg := matchRequestJSON(g, w, prevObj[k], path); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// matchSubset is the outcome rule: every expected object key matches recursively, arrays
// match element by element with the same length, and scalars are equal.
func matchSubset(got, want any, at string) string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s = %s, want an object", at, mustJSON(got))
		}
		for k, v := range w {
			gv, present := g[k]
			if !present {
				return fmt.Sprintf("%s.%s is missing", at, k)
			}
			if msg := matchSubset(gv, v, at+"."+k); msg != "" {
				return msg
			}
		}
		return ""
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return fmt.Sprintf("%s = %s, want %d items", at, mustJSON(got), len(w))
		}
		for i := range w {
			if msg := matchSubset(g[i], w[i], fmt.Sprintf("%s[%d]", at, i)); msg != "" {
				return msg
			}
		}
		return ""
	default:
		if !reflect.DeepEqual(got, want) {
			return fmt.Sprintf("%s = %s, want %s", at, mustJSON(got), mustJSON(want))
		}
		return ""
	}
}

func isSlice(v any) bool {
	_, ok := v.([]any)
	return ok
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(b)
}

// ---- comparable data ----

var (
	durationType   = reflect.TypeOf(time.Duration(0))
	timeType       = reflect.TypeOf(time.Time{})
	rawMessageType = reflect.TypeOf(json.RawMessage(nil))
)

// plain is a Go result as the scenarios compare it: exported struct fields (promoted
// ones included) under snake_case names, numbers as float64, raw JSON decoded, times as
// ISO strings, and a duration field Foo as foo_seconds.
func plain(v any) any {
	return plainValue(reflect.ValueOf(v), 0)
}

func plainValue(v reflect.Value, depth int) any {
	if !v.IsValid() || depth > 8 {
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return plainValue(v.Elem(), depth+1)
	}
	switch v.Type() {
	case rawMessageType:
		var out any
		if len(v.Bytes()) == 0 || json.Unmarshal(v.Bytes(), &out) != nil {
			return nil
		}
		return out
	case timeType:
		return v.Interface().(time.Time).UTC().Format("2006-01-02T15:04:05.000Z")
	case durationType:
		return time.Duration(v.Int()).Seconds()
	}
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint())
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.String:
		return v.String()
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = plainValue(v.Index(i), depth+1)
		}
		return out
	case reflect.Map:
		out := map[string]any{}
		iter := v.MapRange()
		for iter.Next() {
			out[fmt.Sprint(iter.Key().Interface())] = plainValue(iter.Value(), depth+1)
		}
		return out
	case reflect.Struct:
		out := map[string]any{}
		for _, f := range reflect.VisibleFields(v.Type()) {
			if !f.IsExported() || f.Anonymous {
				continue
			}
			fv, err := v.FieldByIndexErr(f.Index)
			if err != nil {
				continue
			}
			name := snakeCase(f.Name)
			if f.Type == durationType {
				name += "_seconds"
			}
			out[name] = plainValue(fv, depth+1)
		}
		return out
	default:
		return nil
	}
}

// snakeCase spells a Go field name the way the wire does: TurnID → turn_id,
// CurrentRuntimeID → current_runtime_id, ETag → etag.
func snakeCase(name string) string {
	if name == "ETag" {
		return "etag"
	}
	rs := []rune(name)
	var b strings.Builder
	for i, r := range rs {
		if unicode.IsUpper(r) {
			if i > 0 && (unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1]) ||
				(unicode.IsUpper(rs[i-1]) && i+1 < len(rs) && unicode.IsLower(rs[i+1]))) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
