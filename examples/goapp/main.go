package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

func main() {
	port := getenv("PORT", "8081")
	upstreamURL := getenv("UPSTREAM_URL", "http://127.0.0.1:"+port+"/upstream")
	proxyURL, err := url.Parse(getenv("OUTBOUND_PROXY", "http://127.0.0.1:8084"))
	if err != nil {
		log.Fatal(err)
	}
	outboundClient := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}

	http.HandleFunc("/upstream", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond) // Baseline dependency latency
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	http.HandleFunc("/api/test", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			q = "world"
		}

		bodyBytes, _ := io.ReadAll(r.Body)

		log.Println("Received request:", q, "with payload size:", len(bodyBytes))

		time.Sleep(500 * time.Millisecond) // Bug: 10x slower than baseline

		resp, err := outboundClient.Get(upstreamURL)
		size := 0

		if err != nil {
			log.Println("Outbound call failed:", err)
		} else {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			size = len(body)
		}

		w.WriteHeader(http.StatusInternalServerError) // Bug: 500 instead of 200
		fmt.Fprintf(w, "Service Error | external payload size=%d bytes\n", size)
	})

	log.Printf("Example Go service listening on :%s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
