package protocol

// DiagnoseReport is the result of network.diagnose: can this PC learn its
// public address, what kind of NAT is it behind, and do the configured TURN
// relays work.
type DiagnoseReport struct {
	// NAT is "open", "cone", "symmetric", "blocked" or "unknown".
	NAT      string `json:"nat"`
	PublicIP string `json:"public_ip,omitempty"`
	// STUN lists every server tried.
	STUN []DiagnoseServer `json:"stun"`
	// Working are the servers that answered, fastest first.
	Working []string `json:"working"`
	// TURN lists the configured relays and whether each gave a relay address.
	TURN []DiagnoseServer `json:"turn"`
	// Advice is a machine-readable hint the UI turns into a sentence:
	// "ok", "needs_turn", "stun_blocked" or "turn_broken".
	Advice string `json:"advice"`
	// DurationMS is how long the check took.
	DurationMS int64 `json:"duration_ms"`
}

// DiagnoseServer is one server's result.
type DiagnoseServer struct {
	Server string `json:"server"`
	OK     bool   `json:"ok"`
	// Mapped is the public address a STUN server saw, or the relay address a
	// TURN server gave.
	Mapped string `json:"mapped,omitempty"`
	RTTMS  int64  `json:"rtt_ms,omitempty"`
	Error  string `json:"error,omitempty"`
}
