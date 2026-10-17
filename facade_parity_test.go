package pinesandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// facadeTypes are the nouns of contract/facade-parity.json, by their Go handle types.
var facadeTypes = map[string]reflect.Type{
	"Client":    reflect.TypeOf(&Client{}),
	"Computer":  reflect.TypeOf(&Computer{}),
	"Skills":    reflect.TypeOf(&Skills{}),
	"Session":   reflect.TypeOf(&Session{}),
	"AgentMode": reflect.TypeOf(&AgentMode{}),
	"DriveMode": reflect.TypeOf(&DriveMode{}),
}

// deprecatedAliases are the released Go names a canonical verb replaced. Each keeps
// working, marked // Deprecated:, until the next pool minor (0.4). The value is the
// canonical Go name it forwards to.
var deprecatedAliases = map[string]string{
	"Client.AttachComputer":        "Client.StartComputer",
	"Computer.SandboxID":           "Computer.RuntimeID",
	"Computer.LatestSnapshot":      "Computer.Snapshot",
	"Computer.Capture":             "Computer.Checkpoint",
	"Computer.ListOrphanDownloads": "Computer.OrphanDownloads",
	"Computer.ListSkills":          "Skills.List",
	"Computer.GetSkill":            "Skills.Get",
	"Computer.SetSkillEnabled":     "Skills.SetEnabled",
	"Computer.ListSkillDrafts":     "Skills.Drafts",
	"Computer.ListSkillVersions":   "Skills.Versions",
	"Computer.GetSkillVersion":     "Skills.Version",
	"Computer.ActivateSkill":       "Skills.Activate",
	"Computer.DeactivateSkill":     "Skills.Deactivate",
	"Computer.DeleteSkillVersion":  "Skills.DeleteVersion",
}

// notVerbs are exported methods that are not facade verbs: property accessors (other
// SDKs expose them as fields or getters), navigation to a sub-facade, fmt hooks, and the
// low-level attach that takes a Client's *Connection.
var notVerbs = map[string]string{
	"Client.String":            "fmt.Stringer (redacted)",
	"Client.GoString":          "fmt.GoStringer (redacted)",
	"Computer.ID":              "accessor",
	"Computer.Key":             "accessor",
	"Computer.BindingRevision": "accessor",
	"Computer.Location":        "accessor",
	"Computer.CaptureKeypair":  "accessor",
	"Computer.ComputerToken":   "accessor",
	"Computer.PersistenceMode": "accessor",
	"Computer.RuntimeID":       "accessor",
	"Computer.Skills":          "navigation to the Skills noun",
	"Computer.String":          "fmt.Stringer (redacted)",
	"Computer.GoString":        "fmt.GoStringer (redacted)",
	"Computer.Attach":          "low-level attach with a Client's *Connection; CreateComputer and StartComputer are the verbs",
	"Session.Name":             "accessor",
	"Session.Token":            "accessor",
	"Session.Info":             "accessor",
	"Session.Agent":            "navigation to the AgentMode noun",
	"Session.Drive":            "navigation to the DriveMode noun",
}

// blockingVerbException is the single recorded exception to the facade contract: the
// canonical learn/teach/refine block until the authoring result, but Go's released verbs
// return the accepted run, and changing that is a breaking change held for the next pool
// minor (0.4). Until then StartLearn/StartTeach/StartRefine are the asynchronous forms
// and the blocking verbs keep their asynchronous Go behavior, marked // Deprecated:.
var blockingVerbException = map[string]string{
	"Session.Learn":  "Session.StartLearn",
	"Session.Teach":  "Session.StartTeach",
	"Session.Refine": "Session.StartRefine",
}

type facadeParityVerb struct {
	Verb       string   `json:"verb"`
	Operations []string `json:"operations"`
	Go         string   `json:"go"`
}

func loadFacadeParity(t *testing.T) []facadeParityVerb {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "contract", "facade-parity.json"))
	if err != nil {
		t.Skipf("facade parity map not present (mirror build): %v", err)
	}
	var p struct {
		Nouns []string           `json:"nouns"`
		Verbs []facadeParityVerb `json:"verbs"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Nouns) != len(facadeTypes) {
		t.Errorf("facade-parity.json nouns %v, the test reflects on %d types", p.Nouns, len(facadeTypes))
	}
	for _, noun := range p.Nouns {
		if facadeTypes[noun] == nil {
			t.Errorf("facade-parity.json noun %s has no Go handle type in the test", noun)
		}
	}
	return p.Verbs
}

func hasMethod(name string) bool {
	typeName, method, ok := strings.Cut(name, ".")
	typ := facadeTypes[typeName]
	if !ok || typ == nil {
		return false
	}
	_, exists := typ.MethodByName(method)
	return exists
}

// TestFacadeParity: every Go name in contract/facade-parity.json is an exported method,
// and every exported method on the facade types is a listed verb, a deprecated alias of
// one, or a recorded non-verb.
func TestFacadeParity(t *testing.T) {
	verbs := loadFacadeParity(t)
	listed := map[string]bool{}
	for _, v := range verbs {
		if v.Go == "" {
			t.Errorf("%s has no Go name", v.Verb)
			continue
		}
		if !hasMethod(v.Go) {
			t.Errorf("%s: Go method %s does not exist", v.Verb, v.Go)
		}
		if listed[v.Go] {
			t.Errorf("%s is listed for two verbs", v.Go)
		}
		listed[v.Go] = true
	}
	for old, canonical := range deprecatedAliases {
		if listed[old] {
			t.Errorf("%s is a deprecated alias but also a listed verb", old)
		}
		if !hasMethod(old) {
			t.Errorf("deprecated alias %s no longer exists; drop it from the test", old)
		}
		if !listed[canonical] && notVerbs[canonical] == "" {
			t.Errorf("deprecated alias %s forwards to %s, which is neither a listed verb nor a recorded non-verb", old, canonical)
		}
	}
	for name := range notVerbs {
		if listed[name] {
			t.Errorf("%s is recorded as a non-verb but is a listed verb", name)
		}
		if !hasMethod(name) {
			t.Errorf("recorded non-verb %s no longer exists; drop it from the test", name)
		}
	}
	var unmapped []string
	for typeName, typ := range facadeTypes {
		for i := 0; i < typ.NumMethod(); i++ {
			name := typeName + "." + typ.Method(i).Name
			if !listed[name] && deprecatedAliases[name] == "" && notVerbs[name] == "" {
				unmapped = append(unmapped, name)
			}
		}
	}
	sort.Strings(unmapped)
	for _, name := range unmapped {
		t.Errorf("%s is public but not a verb in contract/facade-parity.json (add the verb there, or record it here as a deprecated alias or non-verb)", name)
	}
}

// TestFacadeParityOperationsAreSent: every operation server-sdk-operations.json requires
// of Go is sent by some verb facade-parity.json lists for Go.
func TestFacadeParityOperationsAreSent(t *testing.T) {
	verbs := loadFacadeParity(t)
	sent := map[string]bool{}
	for _, v := range verbs {
		if !hasMethod(v.Go) {
			continue // reported by TestFacadeParity
		}
		for _, op := range v.Operations {
			sent[op] = true
		}
	}
	for _, op := range requiredGoOperations(t) {
		if !sent[op] {
			t.Errorf("Go must send %s, but no Go verb in facade-parity.json lists it", op)
		}
	}
}

// TestBlockingVerbExceptionExpires keeps the one recorded exception honest: it names
// listed verbs with an existing asynchronous form, and it fails once the SDK reaches the
// 0.4 pool minor, where Learn/Teach/Refine must block and the exception is removed.
func TestBlockingVerbExceptionExpires(t *testing.T) {
	verbs := loadFacadeParity(t)
	listed := map[string]facadeParityVerb{}
	for _, v := range verbs {
		listed[v.Go] = v
	}
	for blocking, start := range blockingVerbException {
		v, ok := listed[blocking]
		if !ok || !hasMethod(blocking) || !hasMethod(start) {
			t.Errorf("exception %s → %s: both must be listed, existing verbs", blocking, start)
			continue
		}
		if !contains(v.Operations, "streamAuthorEventsV2") {
			t.Errorf("%s no longer blocks in the contract; the exception is obsolete", blocking)
		}
	}
	parts := strings.Split(Version, ".")
	minor, err := strconv.Atoi(parts[1])
	if err != nil || parts[0] != "0" {
		t.Fatalf("unexpected Version %q", Version)
	}
	if minor >= 4 {
		t.Errorf("Version %s reached the 0.4 pool minor: make Session.Learn/Teach/Refine block, remove their deprecation notes and this exception", Version)
	}
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
