package pinesandbox

import (
	"context"
	"strings"
	"testing"
)

func TestPasskeyFacadeRejectsEmptyIdentifiers(t *testing.T) {
	ctx := context.Background()
	computer := &Computer{}
	session := &Session{}

	for name, err := range map[string]error{
		"delete credential": computer.DeletePasskey(ctx, ""),
		"approve ceremony":  computer.ApprovePasskeyCeremony(ctx, ""),
		"decline ceremony":  computer.DeclinePasskeyCeremony(ctx, ""),
		"declare intent":    session.DeclarePasskeyIntent(ctx, ""),
		"session approve":   session.ApprovePasskeyCeremony(ctx, ""),
		"session decline":   session.DeclinePasskeyCeremony(ctx, ""),
	} {
		if err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("%s error = %v, want local required-field validation", name, err)
		}
	}
}
