package pinesandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type admissionTableRow struct {
	State          string  `json:"state"`
	Reason         string  `json:"reason"`
	Start          string  `json:"start"`
	Recover        string  `json:"recover"`
	StartMessage   *string `json:"start_message"`
	RecoverMessage *string `json:"recover_message"`
	Refusal        string  `json:"refusal"`
}

// TestAdmissionMatchesContract checks decideAdmission against
// contract/runtime-admission.json, row by row. The published mirror ships only the
// module, so this runs in the monorepo and skips in a mirror build.
func TestAdmissionMatchesContract(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "contract", "runtime-admission.json"))
	if err != nil {
		t.Skipf("canonical artifact not present (mirror build): %v", err)
	}
	var table struct {
		ReasonAliases map[string]string   `json:"reason_aliases"`
		Messages      map[string]string   `json:"messages"`
		Absent        admissionTableRow   `json:"absent"`
		Rows          []admissionTableRow `json:"rows"`
		Unknown       admissionTableRow   `json:"unknown"`
	}
	if err := json.Unmarshal(b, &table); err != nil {
		t.Fatal(err)
	}
	optional := regexp.MustCompile(`\[([^\]]*)\]`)
	text := func(name *string, state, reason string) string {
		if name == nil {
			return ""
		}
		template := strings.ReplaceAll(table.Messages[*name], "{recover}", "RecoverComputer")
		if reason == "" {
			template = optional.ReplaceAllString(template, "")
		} else {
			template = strings.ReplaceAll(optional.ReplaceAllString(template, "$1"), "{reason}", reason)
		}
		return strings.ReplaceAll(template, "{state}", state)
	}
	check := func(row admissionTableRow, state, reason string) {
		t.Helper()
		refusal := admissionRefusal(row.Refusal)
		if refusal == "" {
			refusal = admissionRefusalRetained
		}
		for _, c := range []struct {
			column  admissionColumn
			action  string
			message *string
		}{{admissionStart, row.Start, row.StartMessage}, {admissionRecover, row.Recover, row.RecoverMessage}} {
			want := admissionDecision{Action: admissionAction(c.action), Message: text(c.message, state, reason), Refusal: refusal}
			if got := decideAdmission(c.column, state, reason); got != want {
				t.Errorf("decideAdmission(%d, %q, %q) = %+v, want %+v", c.column, state, reason, got, want)
			}
		}
	}

	for _, row := range table.Rows {
		if row.Reason == "" { // no reason, or any other reason
			check(row, row.State, "")
			check(row, row.State, "UNLISTED_REASON")
			continue
		}
		check(row, row.State, row.Reason)
		for alias, canonical := range table.ReasonAliases { // under every name the reason has
			if canonical == row.Reason {
				check(row, row.State, alias)
			}
		}
	}
	check(table.Absent, "", "")
	check(table.Unknown, "unlisted_state", "")
	check(table.Unknown, "unlisted_state", "UNLISTED_REASON")
}

// TestNewRuntimeMatchesContract checks decideNewRuntime against the table's new_runtime rows.
func TestNewRuntimeMatchesContract(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "contract", "runtime-admission.json"))
	if err != nil {
		t.Skipf("canonical artifact not present (mirror build): %v", err)
	}
	var table struct {
		NewRuntime struct {
			Rows []struct {
				State     string `json:"state"`
				ReadyWait string `json:"ready_wait"`
			} `json:"rows"`
			Unknown struct {
				ReadyWait string `json:"ready_wait"`
			} `json:"unknown"`
		} `json:"new_runtime"`
	}
	if err := json.Unmarshal(b, &table); err != nil {
		t.Fatal(err)
	}
	for _, row := range table.NewRuntime.Rows {
		if got := decideNewRuntime(row.State); got != newRuntimeAction(row.ReadyWait) {
			t.Errorf("decideNewRuntime(%q) = %q, want %q", row.State, got, row.ReadyWait)
		}
	}
	if got := decideNewRuntime("unlisted_state"); got != newRuntimeAction(table.NewRuntime.Unknown.ReadyWait) {
		t.Errorf("decideNewRuntime(unlisted_state) = %q, want %q", got, table.NewRuntime.Unknown.ReadyWait)
	}
}

// TestAdmissionMessagesMatchContract checks that every message template is generated,
// with and without the reason, {recover} filled.
func TestAdmissionMessagesMatchContract(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "contract", "runtime-admission.json"))
	if err != nil {
		t.Skipf("canonical artifact not present (mirror build): %v", err)
	}
	var table struct {
		Messages map[string]string `json:"messages"`
	}
	if err := json.Unmarshal(b, &table); err != nil {
		t.Fatal(err)
	}
	optional := regexp.MustCompile(`\[([^\]]*)\]`)
	for id, template := range table.Messages {
		text := strings.ReplaceAll(template, "{recover}", "RecoverComputer")
		want := admissionMessage{optional.ReplaceAllString(text, "$1"), optional.ReplaceAllString(text, "")}
		if got, ok := admissionMessages[id]; !ok || got != want {
			t.Errorf("admissionMessages[%q] = %+v, want %+v", id, got, want)
		}
	}
	if len(admissionMessages) != len(table.Messages) {
		t.Errorf("admissionMessages has %d templates, the table %d", len(admissionMessages), len(table.Messages))
	}
}
