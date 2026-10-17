package pinesandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// implementedServerSDKOperations is the Go facade's explicit OpenAPI coverage
// catalog: each public operation the contract requires of Go, mapped to the facade
// method that sends it. The generated artifact owns which operations are required;
// this catalog must advance with the actual implementation in the same change.
// facade_parity_test.go separately checks that contract/facade-parity.json names a Go
// verb sending each required operation.
var implementedServerSDKOperations = map[string]any{
	// control-plane-auth, computers, attach-credentials, bind, locations
	"mintControlToken":       (*Computer).Status, // every lifecycle request mints the project token first
	"getComputerBinding":     (*Client).StartComputer,
	"registerComputer":       (*Client).RegisterComputer,
	"deleteComputer":         (*Client).DeleteComputer,
	"listAvailableLocations": (*Client).AvailableLocations,
	"getProjectPolicy":       (*Client).ProjectPolicy,
	"mintAttachCredentials":  (*Client).CreateComputer,
	"getBindPubkey":          (*Client).CreateComputer,
	"bindComputer":           (*Client).CreateComputer,
	"createDiagnostic":       (*Client).CreateDiagnostic,
	"getDiagnostic":          (*Client).GetDiagnostic,

	// lifecycle
	"createComputer":         (*Client).CreateComputer,
	"getComputer":            (*Computer).Status,
	"destroyComputer":        (*Computer).Kill,
	"stopComputerRuntime":    (*Computer).Stop,
	"pauseComputer":          (*Computer).Pause,
	"resumeComputer":         (*Computer).Resume,
	"setComputerIdleTimeout": (*Computer).SetIdleTimeout,

	// sessions
	"createSession":           (*Computer).CreateSession,
	"getSession":              (*Computer).Session,
	"listSessions":            (*Computer).Sessions,
	"deleteSession":           (*Computer).DestroySession,
	"sessionEpoch":            (*Session).Epoch,
	"sessionFocus":            (*Session).Focus,
	"sessionTerminalRecreate": (*Session).RecreateTerminal,

	// drive
	"sessionExec":    (*Session).Exec,
	"tabsList":       (*Session).ListTabs,
	"tabsCreate":     (*Session).CreateTab,
	"tabsPatch":      (*Session).PatchTab,
	"tabsDelete":     (*Session).CloseTab,
	"computerUse":    (*DriveMode).ComputerUse,
	"observeSession": (*DriveMode).Observe,
	"uploadFile":     (*DriveMode).UploadFile,

	// files and artifacts
	"listSessionFiles":     (*Session).ListFiles,
	"getSessionFile":       (*Session).ReadFile,
	"putSessionAttachment": (*Session).WriteFile,
	"listArtifacts":        (*Session).ListArtifacts,
	"getArtifact":          (*Session).ReadArtifact,
	"zipArtifacts":         (*Session).ZipArtifacts,
	"uploadArtifact":       (*Session).UploadArtifact,

	// agent
	"runAgentV2":           (*AgentMode).Run,
	"getAgentTurnV2":       (*AgentMode).Wait,
	"answerAgentV2":        (*AgentMode).Answer,
	"steerAgentV2":         (*AgentMode).Steer,
	"cancelAgentV2":        (*AgentMode).Cancel,
	"resetAgentThreadV2":   (*AgentMode).Reset,
	"getAgentV2":           (*AgentMode).Status,
	"getAgentTurnsV2":      (*AgentMode).Turns,
	"getAgentEventsV2":     (*AgentMode).Events,
	"getAgentEventsPageV2": (*AgentMode).EventsPage,

	// control and handoffs
	"getControl":        (*Session).ControlState,
	"patchControl":      (*Session).UpdateControl,
	"postControlNotify": (*Session).NotifyHuman,
	"getControlEvents":  (*Session).ControlEvents,
	"listHandoffs":      (*Session).ListHandoffs,
	"getHandoff":        (*Session).GetHandoff,
	"mintDesktopToken":  (*Session).DesktopToken,

	// skills
	"getCapabilities":      (*Computer).Capabilities,
	"listSkills":           (*Skills).List,
	"getSkill":             (*Skills).Get,
	"setSkillEnabled":      (*Skills).SetEnabled,
	"listSkillDrafts":      (*Skills).Drafts,
	"listAllSkillVersions": (*Skills).Versions,
	"listSkillVersions":    (*Skills).Versions,
	"getSkillVersion":      (*Skills).Version,
	"activateSkill":        (*Skills).Activate,
	"deactivateSkill":      (*Skills).Deactivate,
	"deleteSkillVersion":   (*Skills).DeleteVersion,
	"learnSkill":           (*Session).StartLearn,
	"teachSkill":           (*Session).StartTeach,
	"refineSkill":          (*Session).StartRefine,
	"authorSkillDraft":     (*Session).AuthorSkill,
	"streamAuthorEventsV2": (*Session).AuthorEvents,
	"cancelAuthorRun":      (*Session).CancelAuthor,

	// admin: state and orphans
	"getState":              (*Computer).Snapshot,
	"captureCheckpoint":     (*Computer).Checkpoint,
	"listOrphans":           (*Computer).OrphanTabs,
	"deleteOrphan":          (*Computer).CloseOrphanTab,
	"listDownloadOrphans":   (*Computer).OrphanDownloads,
	"claimDownloadOrphan":   (*Computer).ClaimOrphanDownload,
	"discardDownloadOrphan": (*Computer).DiscardOrphanDownload,

	// passkeys
	"listPasskeys":                 (*Computer).ListPasskeys,
	"deletePasskey":                (*Computer).DeletePasskey,
	"listPasskeyCeremonies":        (*Computer).PendingPasskeyCeremonies,
	"decidePasskeyCeremony":        (*Computer).ApprovePasskeyCeremony,
	"declareSessionPasskeyIntent":  (*Session).DeclarePasskeyIntent,
	"listSessionPasskeyCeremonies": (*Session).PendingPasskeyCeremonies,
	"decideSessionPasskeyCeremony": (*Session).ApprovePasskeyCeremony,
}

func requiredGoOperations(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "contract", "server-sdk-operations.json"))
	if err != nil {
		t.Skipf("server SDK operation artifact not present (mirror build): %v", err)
	}
	var required map[string][]struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(b, &required); err != nil {
		t.Fatalf("parse server SDK operation artifact: %v", err)
	}
	ids := make([]string, 0, len(required["go"]))
	for _, operation := range required["go"] {
		ids = append(ids, operation.OperationID)
	}
	return ids
}

func TestServerSDKOperationsConformToSpec(t *testing.T) {
	required := requiredGoOperations(t)
	want := map[string]bool{}
	for _, id := range required {
		want[id] = true
		if implementedServerSDKOperations[id] == nil {
			t.Errorf("OpenAPI requires Go server SDK operation %q, but its facade coverage is missing", id)
		}
	}
	// A catalog entry for an operation the contract no longer requires of Go is stale:
	// either the operation left the public API or it moved out of a Go tag.
	for id := range implementedServerSDKOperations {
		if !want[id] {
			t.Errorf("coverage catalog lists %q, which server-sdk-operations.json does not require of Go", id)
		}
	}
}
