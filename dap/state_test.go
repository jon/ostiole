package dap

import "testing"

func TestDebugPortStateLosesFramingOnce(t *testing.T) {
	state := debugPortState{
		session:  sessionConnected,
		response: responseSimple,
		selectDP: selectState{value: 0x120000f2, valid: true},
	}

	state.loseFraming()
	if state.session != sessionRepairRequired {
		t.Fatalf("session = %v, want repair required", state.session)
	}
	if state.response != responseLost {
		t.Fatalf("response = %v, want lost", state.response)
	}
	if state.selectDP.valid {
		t.Fatal("SELECT remained valid after framing loss")
	}
	if state.apGeneration != 1 {
		t.Fatalf("AP generation = %d, want 1", state.apGeneration)
	}

	state.loseFraming()
	if state.apGeneration != 1 {
		t.Fatalf("AP generation after repeated loss = %d, want 1", state.apGeneration)
	}
	state.forgetResponse()
	if state.response != responseLost {
		t.Fatalf("response after reset attempt = %v, want lost", state.response)
	}
}

func TestDebugPortStateTracksFullSELECT(t *testing.T) {
	var state debugPortState
	state.recordSELECT(0x120000f2)

	if !state.selectDP.valid || state.selectDP.value != 0x120000f2 {
		t.Fatalf("SELECT = %#08x, %t", state.selectDP.value, state.selectDP.valid)
	}
	if state.dpBank() != 2 {
		t.Fatalf("DPBANKSEL = %d, want 2", state.dpBank())
	}
}

func TestDebugPortStateCompletesLifecycle(t *testing.T) {
	state := debugPortState{
		session:    sessionRepairRequired,
		response:   responseSimple,
		ownedPower: powerRequests,
	}
	state.completeConnect()
	if state.session != sessionConnected {
		t.Fatalf("session after connection = %v, want connected", state.session)
	}

	state.beginRepair()
	state.completeRelease()
	if state.session != sessionIdle {
		t.Fatalf("session after release = %v, want idle", state.session)
	}
	if state.ownedPower != 0 {
		t.Fatalf("owned power after release = %#08x, want 0", state.ownedPower)
	}
}
