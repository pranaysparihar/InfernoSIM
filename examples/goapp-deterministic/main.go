package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	port := getenv("PORT", "8084")

	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DisableKeepAlives:   true,
		MaxIdleConns:        0,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     10 * time.Millisecond,
	}

	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: transport,
	}

	http.HandleFunc("/api/demo", func(w http.ResponseWriter, r *http.Request) {
		req, err := http.NewRequest(http.MethodGet, "http://worldtimeapi.org/api/timezone/Etc/UTC", nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("deterministic-go: ok\n"))
	})

	log.Printf("Deterministic Go app listening on :%s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
