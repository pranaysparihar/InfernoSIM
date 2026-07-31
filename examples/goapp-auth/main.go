package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	tokens       = make(map[string]bool)
	tokensMu     sync.RWMutex
	driftEnabled atomic.Bool
)

func main() {
	proxyURL, err := url.Parse(getenv("OUTBOUND_PROXY", "http://127.0.0.1:8084"))
	if err != nil {
		log.Fatal(err)
	}
	outboundClient := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}

	http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		token := "tok_alice_captured_secret_xyz"
		if driftEnabled.Load() {
			token = "tok_alice_drifted_live_9999"
		}
		tokensMu.Lock()
		tokens[token] = true
		tokensMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": token})
	})

	http.HandleFunc("/orders", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		tokensMu.RLock()
		valid := tokens[token]
		tokensMu.RUnlock()
		if !valid {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if driftEnabled.Load() {
			time.Sleep(600 * time.Millisecond) // Latency regression
		}

		// Make outbound dependency call to port 8084 (captured as outbound call by InfernoSIM)
		resp, err := outboundClient.Get(getenv("UPSTREAM_URL", "http://127.0.0.1:8085/upstream"))
		if err != nil {
			log.Println("Outbound call failed:", err)
		} else {
			resp.Body.Close()
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"order_id": "ord_999", "status": "confirmed"}`))
	})

	http.HandleFunc("/orders/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !strings.HasSuffix(path, "/receipt") {
			http.NotFound(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		tokensMu.RLock()
		valid := tokens[token]
		tokensMu.RUnlock()
		if !valid {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if driftEnabled.Load() {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError) // Status mismatch
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"receipt_id": "rec_888", "amount": 100}`))
	})

	http.HandleFunc("/upstream", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream ok"))
	})

	http.HandleFunc("/simulate-drift", func(w http.ResponseWriter, r *http.Request) {
		driftEnabled.Store(true)
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("Drift enabled\n"))
	})

	log.Println("Auth mock app listening on :8085")
	log.Fatal(http.ListenAndServe(":8085", nil))
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
