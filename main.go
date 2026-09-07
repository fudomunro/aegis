package main

import (
	"flag"
	"log"
	"net/http"
	"os"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	apiKey := flag.String("api-key", "", "API key for authenticating Aperture hooks (reads APERTURE_GUARDIAN_API_KEY env if empty)")
	configPath := flag.String("config", "guardian.json", "path to rules configuration file")
	flag.Parse()

	if *apiKey == "" {
		*apiKey = os.Getenv("APERTURE_GUARDIAN_API_KEY")
	}
	if *apiKey == "" {
		log.Fatal("api-key is required (flag or APERTURE_GUARDIAN_API_KEY env)")
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	srv := &Server{
		apiKey: *apiKey,
		config: cfg,
		cache:  NewCache(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pre-request", srv.AuthMiddleware(srv.HandlePreRequest))

	log.Printf("aperture-guardian listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
