package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	port := getenv("PORT", "8081")

	http.HandleFunc("/api/test", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			q = "world"
		}

		log.Println("Received request:", q)

		time.Sleep(100 * time.Millisecond)

		resp, err := http.Get("http://worldtimeapi.org/api/timezone/Etc/UTC")
		size := 0

		if err != nil {
			log.Println("Outbound call failed:", err)
		} else {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			size = len(body)
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Hello %s | external payload size=%d bytes\n", q, size)
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