package dap

type debugPortSession uint8

const (
	sessionIdle debugPortSession = iota
	sessionConnected
	sessionRepairRequired
)

type swdResponseState uint8

const (
	responseUnchecked swdResponseState = iota
	responseSimple
	responseOverrun
	responseLost
)

type selectState struct {
	value uint32
	valid bool
}

type debugPortState struct {
	session      debugPortSession
	response     swdResponseState
	selectDP     selectState
	ownedPower   uint32
	apGeneration uint64
}

func (s *debugPortState) recordSELECT(value uint32) {
	s.selectDP = selectState{value: value, valid: true}
}

func (s *debugPortState) beginProtocolEntry() {
	s.response = responseUnchecked
	s.selectDP.valid = false
}

func (s *debugPortState) forgetResponse() {
	if s.response != responseLost {
		s.response = responseUnchecked
	}
}

func (s *debugPortState) dpBank() uint8 {
	return uint8(s.selectDP.value & 0x0f)
}

func (s *debugPortState) confirmResponse(state uint32) {
	if state&overrunDetect == 0 {
		s.response = responseSimple
		return
	}
	s.response = responseOverrun
}

func (s *debugPortState) invalidateAP() {
	s.apGeneration++
}

func (s *debugPortState) loseFraming() {
	if s.response != responseLost {
		s.invalidateAP()
	}
	s.session = sessionRepairRequired
	s.response = responseLost
	s.selectDP.valid = false
}

func (s *debugPortState) beginRepair() {
	s.session = sessionRepairRequired
}

func (s *debugPortState) ownPower(requests uint32) {
	s.ownedPower = requests
}

func (s *debugPortState) clearOwnedPower() {
	s.ownedPower = 0
}

func (s *debugPortState) completeConnect() {
	s.session = sessionConnected
}

func (s *debugPortState) completeRelease() {
	s.session = sessionIdle
	s.ownedPower = 0
}
