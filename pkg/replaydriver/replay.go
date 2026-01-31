package replaydriver

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"infernosim/pkg/event"
)

func Replay(inboundLog string, target string) ([32]byte, error) {
	f, err := os.Open(inboundLog)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	h := sha256.New()

	client := &http.Client{}

	for dec.More() {
		var e event.Event
		dec.Decode(&e)
		if e.Type != "InboundRequest" {
			continue
		}

		req, _ := http.NewRequest(e.Method, target+e.URL, nil)
		resp, err := client.Do(req)
		if err != nil {
			return [32]byte{}, err
		}
		resp.Body.Close()

		h.Write([]byte(e.Method))
		h.Write([]byte(e.URL))
		h.Write([]byte(fmt.Sprint(resp.StatusCode)))
	}

	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

func Verify(inbound string, target string, runs int) error {
	var ref [32]byte
	for i := 0; i < runs; i++ {
		fp, err := Replay(inbound, target)
		if err != nil {
			return err
		}
		if i == 0 {
			ref = fp
		} else if fp != ref {
			return fmt.Errorf("non-deterministic replay")
		}
		time.Sleep(0) // logical only
	}
	return nil
}