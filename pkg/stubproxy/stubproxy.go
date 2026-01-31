package stubproxy

import (
	"encoding/json"
	"net/http"
	"os"

	"infernosim/pkg/event"
)

type StubProxy struct {
	events []event.Event
	index  int
}

func New(logFile string) (*StubProxy, error) {
	f, err := os.Open(logFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []event.Event
	dec := json.NewDecoder(f)
	for dec.More() {
		var e event.Event
		if err := dec.Decode(&e); err != nil {
			return nil, err
		}
		if e.Type == "OutboundCall" {
			events = append(events, e)
		}
	}
	return &StubProxy{events: events}, nil
}

func (s *StubProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.index >= len(s.events) {
		http.Error(w, "unexpected outbound call", 500)
		os.Exit(1) // HARD FAIL — proof of isolation
	}

	ev := s.events[s.index]
	s.index++

	if ev.Error != "" {
		http.Error(w, ev.Error, 502)
		return
	}

	w.WriteHeader(ev.Status)
}