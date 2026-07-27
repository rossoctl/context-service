package main

import (
	"log"
	"net/http"
	"os"

	"github.com/rossoctl/context-service/internal/api"
	"github.com/rossoctl/context-service/internal/kube"
)

func main() {
	config, err := kube.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}

	manager, err := kube.NewManager(config)
	if err != nil {
		log.Fatal(err)
	}

	addr := envOr("CS_LISTEN_ADDR", ":8080")
	server := &http.Server{
		Addr:              addr,
		Handler:           api.NewHandler(manager),
		ReadHeaderTimeout: api.ReadHeaderTimeout,
	}

	log.Printf("context-service listening on %s (namespace=%s)", addr, config.Namespace)
	log.Fatal(server.ListenAndServe())
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
