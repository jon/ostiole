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
	session        debugPortSession
	response       swdResponseState
	selectDP       selectState
	priorSELECT    selectState
	selectPending  bool
	dpWritePending bool
	ownedPower     uint32
	apGeneration   uint64
}

func (s *debugPortState) recordSELECT(value uint32) {
	s.priorSELECT = s.selectDP
	s.selectDP = selectState{value: value, valid: true}
	s.selectPending = true
}

func (s *debugPortState) confirmSELECT() {
	s.priorSELECT = selectState{}
	s.selectPending = false
}

func (s *debugPortState) invalidateSELECT() {
	s.selectDP = selectState{}
	s.confirmSELECT()
}

func (s *debugPortState) beginProtocolEntry() {
	s.response = responseUnchecked
	s.invalidateSELECT()
	s.settleDPWrite()
}

func (s *debugPortState) beginConnect() {
	s.session = sessionRepairRequired
	s.beginProtocolEntry()
}

func (s *debugPortState) dpBank() uint8 {
	return uint8(s.selectDP.value & 0x0f)
}

func (s *debugPortState) faultBankZero() bool {
	if !s.selectDP.valid || s.dpBank() != 0 {
		return false
	}
	return !s.selectPending || s.priorSELECT.valid && s.priorSELECT.value&0x0f == 0
}

func (s *debugPortState) resolveSELECTFromCTRLSTAT(ctrlStat uint32) {
	if !s.selectPending {
		return
	}
	if ctrlStat&writeDataError != 0 {
		s.invalidateSELECT()
		return
	}
	s.confirmSELECT()
}

func (s *debugPortState) dpBankAmbiguous() bool {
	return s.selectPending && (!s.priorSELECT.valid || s.priorSELECT.value&0x0f != s.selectDP.value&0x0f)
}

func (s *debugPortState) confirmResponse(state uint32) {
	if state&overrunDetect == 0 {
		s.response = responseSimple
		return
	}
	s.response = responseOverrun
}

func (s *debugPortState) responseKnown() bool {
	return s.response == responseSimple || s.response == responseOverrun
}

func (s *debugPortState) invalidateAP() {
	s.apGeneration++
}

func (s *debugPortState) beginDPWrite() {
	s.dpWritePending = true
}

func (s *debugPortState) settleDPWrite() {
	s.dpWritePending = false
}

func (s *debugPortState) loseFraming() {
	if s.response != responseLost {
		s.invalidateAP()
	}
	s.session = sessionRepairRequired
	s.response = responseLost
	s.invalidateSELECT()
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
	s.settleDPWrite()
}
