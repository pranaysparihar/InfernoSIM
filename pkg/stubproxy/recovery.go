package stubproxy

import "net/http"

func (s *StubProxy) serveRecovery(w http.ResponseWriter, r *http.Request, body []byte) {
	status, headers, response, err := s.recovery.Respond(r.Context(), body)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	// Live responses pass through the same explicit reliability engine. The
	// mode opens model inference only; tool responses still need recorded or
	// caller-configured scenario matches.
	if s.agentEngine != nil {
		decision, err := s.processAgent(r, body, status, headers, response)
		if err != nil {
			s.agentFailure(w, err)
			return
		}
		if s.writeAgentDecision(r.Context(), w, decision, nil, "", nil) {
			return
		}
	}
	copyHeaders(w.Header(), headers)
	w.WriteHeader(status)
	_, _ = w.Write(response)
}
