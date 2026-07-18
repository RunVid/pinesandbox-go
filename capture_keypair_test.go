package pinesandbox

import (
	"context"
	"testing"
)

func TestAttachOptionsInvalidRevisionDoesNotMutateCaptureKeypairs(t *testing.T) {
	current, err := GenerateCaptureKeypair(2)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := GenerateCaptureKeypair(1)
	if err != nil {
		t.Fatal(err)
	}
	computer := newComputer("0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000", make([]byte, 32))

	// An invalid revision stops before the Connection is used, letting this
	// test pin the high-level option plumbing without provisioning a pod.
	err = computer.Attach(context.Background(), nil, AttachOptions{
		CaptureKeypair:       current,
		PriorCaptureKeypairs: []*CaptureKeypair{prior},
		BindingRevision:      -1,
	})
	if err == nil {
		t.Fatal("Attach with a negative binding revision succeeded")
	}

	keypairs, generation := computer.captureKeypairsCopy()
	if generation != 0 || len(keypairs) != 0 {
		t.Fatalf("invalid attach mutated capture config: generation=%d keypairs=%#v", generation, keypairs)
	}
}

func TestConfigureCaptureKeypairsIsAtomic(t *testing.T) {
	current, err := GenerateCaptureKeypair(2)
	if err != nil {
		t.Fatal(err)
	}
	invalidPrior, err := GenerateCaptureKeypair(3)
	if err != nil {
		t.Fatal(err)
	}
	computer := newComputer("0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000", make([]byte, 32))

	err = computer.configureCaptureKeypairs(current, []*CaptureKeypair{invalidPrior})
	if err == nil {
		t.Fatal("configuration accepted a prior generation above the current generation")
	}
	keypairs, generation := computer.captureKeypairsCopy()
	if generation != 0 || len(keypairs) != 0 {
		t.Fatalf("failed configuration partially mutated Computer: generation=%d keypairs=%#v", generation, keypairs)
	}
}

func TestCaptureKeypairGenerationCannotChangeMaterial(t *testing.T) {
	current, err := GenerateCaptureKeypair(2)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := GenerateCaptureKeypair(2)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := GenerateCaptureKeypair(1)
	if err != nil {
		t.Fatal(err)
	}
	priorReplacement, err := GenerateCaptureKeypair(1)
	if err != nil {
		t.Fatal(err)
	}
	computer := newComputer("0190aaaa-bbbb-7ccc-8ddd-eeeeffff0000", make([]byte, 32))
	if err := computer.SetCaptureKeypair(current); err != nil {
		t.Fatal(err)
	}
	if err := computer.SetCaptureKeypair(replacement); err == nil {
		t.Fatal("same generation accepted different current key material")
	}
	if err := computer.AddPriorCaptureKeypair(prior); err != nil {
		t.Fatal(err)
	}
	if err := computer.AddPriorCaptureKeypair(priorReplacement); err == nil {
		t.Fatal("same generation accepted different prior key material")
	}
}

func TestCreateComputerValidatesCaptureKeypairBeforeRegistration(t *testing.T) {
	credentials, err := GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	// A nil provider would panic if registration were reached. Capture material
	// must be validated first so a malformed key cannot leave an ownership-row
	// side effect without a usable Computer.
	client := &Client{conn: &Connection{}}
	_, err = client.CreateComputer(context.Background(), AttachOptions{
		Credentials:    credentials,
		CaptureKeypair: &CaptureKeypair{Generation: 0},
	})
	if err == nil {
		t.Fatal("CreateComputer accepted an invalid capture keypair")
	}
}
