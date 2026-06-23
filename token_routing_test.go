package pinesandbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestFacade_TokenRouting_Comprehensive asserts the ct_ vs ps_ choice for EVERY facade
// method — the facade's core authorization decision. A coord stub records the X-Pine-Auth
// it received per (method, path); we call every wrapper (ignoring response-parse errors —
// only the sent token matters) and check each against its expected token class:
//   - Computer-level ops + agent MUTATIONS + desktop-token mint → the Computer's ct_
//   - session-scoped reads + drive + files/artifacts/tabs/control/skills-authoring → ps_
//   - health/metrics → token-less
//
// A wrong token in any single wrapper fails here.
func TestFacade_TokenRouting_Comprehensive(t *testing.T) {
	const ct, ps = "ct_tok", "ps_tok"
	var mu sync.Mutex
	auth := map[string]string{}
	record := func(r *http.Request) {
		mu.Lock()
		auth[r.Method+" "+r.URL.Path] = r.Header.Get("X-Pine-Auth")
		mu.Unlock()
	}
	coordSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if r.URL.Path == "/sessions" && r.Method == "POST" {
			fmt.Fprintf(w, `{"session":{"name":"s1","token":%q}}`, ps) // give the Session its ps_
			return
		}
		fmt.Fprint(w, `{}`) // minimal; parse may fail downstream — we only assert the token
	}))
	defer coordSrv.Close()

	conn := buildTestConnection(t, "http://unused", coordSrv.URL)
	comp := newComputer("c1", make([]byte, 32))
	if err := comp.adopt(conn, "sb-1", ct, "running"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sess, err := comp.CreateSession(ctx, CreateSessionOptions{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	noBytes := func([]byte) error { return nil }
	noEvent := func(map[string]any) error { return nil }

	// Call every wrapper (errors ignored — the request + its token were already sent).
	_, _ = sess.Exec(ctx, "x", ExecOptions{}, noEvent)
	_, _ = sess.ListFiles(ctx, ListFilesOptions{})
	_, _ = sess.ReadFile(ctx, "f")
	_, _ = sess.WriteFile(ctx, "f", []byte("b"))
	_, _ = sess.ListArtifacts(ctx, "")
	_, _ = sess.ReadArtifact(ctx, "a1")
	_, _ = sess.ZipArtifacts(ctx, "")
	_, _ = sess.UploadArtifact(ctx, "o", []byte("b"))
	_, _ = sess.ListTabs(ctx)
	_, _ = sess.CreateTab(ctx, "https://x", "")
	_, _ = sess.PatchTab(ctx, "t1", PatchTabOptions{})
	_ = sess.CloseTab(ctx, "t1")
	_, _ = sess.ControlState(ctx)
	_, _ = sess.UpdateControl(ctx, map[string]any{}, PatchControlOptions{IfMatch: "v1"})
	_ = sess.NotifyHuman(ctx, "r", "", "v1")
	_, _ = sess.ListHandoffs(ctx, 0, "")
	_, _ = sess.GetHandoff(ctx, "h1")
	_, _ = sess.ControlEvents(ctx, "", noBytes)
	_, _ = sess.Epoch(ctx)
	_ = sess.Focus(ctx)
	_ = sess.RecreateTerminal(ctx)
	_, _ = sess.Learn(ctx, "")
	_, _ = sess.Teach(ctx, "g", TeachOptions{})
	_, _ = sess.AuthorSkill(ctx, "sk", "md", "why", AuthorSkillOptions{})
	_, _ = sess.AuthorEvents(ctx, "au1", "", noBytes)
	_, _ = sess.CancelAuthor(ctx, "au1")
	_, _ = sess.Agent().Status(ctx)
	_, _ = sess.Agent().Result(ctx)
	_, _ = sess.Agent().Events(ctx, "", noBytes)
	_, _ = sess.Drive().Observe(ctx)
	_, _ = sess.Drive().ComputerUse(ctx, "click", nil)
	_, _ = sess.Drive().UploadFile(ctx, "#f", "p")
	// agent mutations + desktop-token → ct_
	_, _ = sess.Agent().Run(ctx, "g", RunOptions{})
	_, _ = sess.Agent().Steer(ctx, "s", SteerOptions{})
	_, _ = sess.Agent().Answer(ctx, "rq", "ans", "")
	_, _ = sess.Agent().Cancel(ctx)
	_, _ = sess.Agent().Reset(ctx)
	_, _ = sess.DesktopToken(ctx)
	// Computer-level → ct_
	_, _ = comp.Session(ctx, "s1")
	_, _ = comp.Sessions(ctx)
	_ = comp.DestroySession(ctx, "s2", false)
	_, _ = comp.ListSkills(ctx)
	_, _ = comp.GetSkill(ctx, "sk1")
	_, _ = comp.ListSkillDrafts(ctx)
	_, _ = comp.ListSkillVersions(ctx, "")
	_, _ = comp.ListSkillVersions(ctx, "sk1")
	_, _ = comp.GetSkillVersion(ctx, "sk1", "1")
	_, _ = comp.ActivateSkill(ctx, "sk1", "1")
	_, _ = comp.DeactivateSkill(ctx, "sk1")
	_, _ = comp.DeleteSkillVersion(ctx, "sk1", "1")
	_, _ = comp.LatestSnapshot(ctx)
	_, _ = comp.Capture(ctx)
	_, _ = comp.ListOrphanDownloads(ctx)
	_, _ = comp.ClaimOrphanDownload(ctx, "g1", "s1", "")
	_ = comp.DiscardOrphanDownload(ctx, "g1")
	// token-less
	_, _ = comp.Health(ctx)
	_, _ = comp.Metrics(ctx)

	expect := map[string]string{
		// ps_ (session-scoped)
		"POST /sessions/s1/exec":                        ps,
		"GET /v1/sessions/s1/files/list":                ps,
		"GET /v1/sessions/s1/files":                     ps,
		"PUT /v1/sessions/s1/files":                     ps,
		"GET /v1/sessions/s1/artifacts":                 ps,
		"GET /v1/sessions/s1/artifacts/a1":              ps,
		"GET /v1/sessions/s1/artifacts/zip":             ps,
		"POST /v1/sessions/s1/artifacts":                ps,
		"GET /sessions/s1/tabs":                         ps,
		"POST /sessions/s1/tabs":                        ps,
		"PATCH /sessions/s1/tabs/t1":                    ps,
		"DELETE /sessions/s1/tabs/t1":                   ps,
		"GET /v1/sessions/s1/control":                   ps,
		"PATCH /v1/sessions/s1/control":                 ps,
		"POST /v1/sessions/s1/control/notify":           ps,
		"GET /v1/sessions/s1/handoffs":                  ps,
		"GET /v1/sessions/s1/handoffs/h1":               ps,
		"GET /v1/sessions/s1/control/events":            ps,
		"GET /sessions/s1/epoch":                        ps,
		"POST /sessions/s1/focus":                       ps,
		"POST /sessions/s1/terminal/recreate":           ps,
		"POST /v1/sessions/s1/learn":                    ps,
		"POST /v1/sessions/s1/teach":                    ps,
		"POST /v1/sessions/s1/skills":                   ps,
		"GET /v1/sessions/s1/skills/author/au1/events":  ps,
		"POST /v1/sessions/s1/skills/author/au1/cancel": ps,
		"GET /v1/sessions/s1/agent":                     ps,
		"GET /v1/sessions/s1/agent/result":              ps,
		"GET /v1/sessions/s1/agent/events":              ps,
		"POST /v1/sessions/s1/observe":                  ps,
		"POST /v1/sessions/s1/computer-use":             ps,
		"POST /v1/sessions/s1/upload_file":              ps,
		// ct_ (agent mutations + desktop-token)
		"POST /v1/sessions/s1/agent/run":     ct,
		"POST /v1/sessions/s1/agent/steer":   ct,
		"POST /v1/sessions/s1/agent/answer":  ct,
		"POST /v1/sessions/s1/agent/cancel":  ct,
		"POST /v1/sessions/s1/agent/reset":   ct,
		"POST /v1/sessions/s1/desktop-token": ct,
		// ct_ (Computer-level)
		"POST /sessions":                   ct,
		"GET /sessions/s1":                 ct,
		"GET /sessions":                    ct,
		"DELETE /sessions/s2":              ct,
		"GET /v1/skills":                   ct,
		"GET /v1/skills/sk1":               ct,
		"GET /v1/skills/drafts":            ct,
		"GET /v1/skills/versions":          ct,
		"GET /v1/skills/sk1/versions":      ct,
		"GET /v1/skills/sk1/versions/1":    ct,
		"POST /v1/skills/sk1/activate":     ct,
		"POST /v1/skills/sk1/deactivate":   ct,
		"DELETE /v1/skills/sk1/versions/1": ct,
		"GET /state":                       ct,
		"POST /v1/coord/capture":           ct,
		"GET /downloads/orphans":           ct,
		"POST /downloads/orphans/g1/claim": ct,
		"DELETE /downloads/orphans/g1":     ct,
		// token-less
		"GET /health":  "",
		"GET /metrics": "",
	}

	mu.Lock()
	defer mu.Unlock()
	for key, want := range expect {
		got, called := auth[key]
		if !called {
			t.Errorf("%s was never called (routing table drift?)", key)
			continue
		}
		if got != want {
			t.Errorf("%s used token %q, want %q", key, tokClass(got), tokClass(want))
		}
	}
}

func tokClass(tok string) string {
	switch tok {
	case "":
		return "<none>"
	case "ct_tok":
		return "ct_"
	case "ps_tok":
		return "ps_"
	default:
		return tok
	}
}
