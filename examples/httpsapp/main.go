package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

func main() {
	mode := flag.String("mode", "server", "Mode: server or client")
	addr := flag.String("addr", ":8443", "Address to listen or connect to")
	proxyUrl := flag.String("proxy", "", "Proxy URL (for client, e.g. http://localhost:9000)")
	flag.Parse()

	if *mode == "server" {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/secure", func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			log.Printf("Received secure request with body: %s", string(body))
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Acknowledged secure payload"))
		})

		// For simplicity in the example, we use the MITM CA to serve the server as well,
		// though normally the server has its own cert. This just ensures local availability.
		home, _ := os.UserHomeDir()
		certPath := filepath.Join(home, ".infernosim", "ca", "infernosim-ca.crt")
		keyPath := filepath.Join(home, ".infernosim", "ca", "infernosim-ca.key")

		server := &http.Server{
			Addr:    *addr,
			Handler: mux,
		}

		log.Printf("HTTPS Example Server listening on %s", *addr)
		if err := server.ListenAndServeTLS(certPath, keyPath); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	} else {
		// client
		home, _ := os.UserHomeDir()
		caCertPath := filepath.Join(home, ".infernosim", "ca", "infernosim-ca.crt")
		caCert, err := os.ReadFile(caCertPath)
		if err != nil {
			log.Fatalf("Failed to read CA cert (run infernosim --https-mode=mitm first): %v", err)
		}

		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(caCert)

		transport := &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: caCertPool,
			},
		}

		if *proxyUrl != "" {
			proxyURL, _ := url.Parse(*proxyUrl)
			transport.Proxy = http.ProxyURL(proxyURL)
		}

		client := &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second,
		}

		req, err := http.NewRequest("GET", "https://localhost"+*addr+"/api/secure", nil)
		if err != nil {
			log.Fatalf("Request build failed: %v", err)
		}

		log.Printf("Client sending request to https://localhost%s via proxy %s", *addr, *proxyUrl)
		resp, err := client.Do(req)
		if err != nil {
			log.Fatalf("Client request failed: %v", err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("Client received [%d]: %s\n", resp.StatusCode, string(body))
	}
}
