// Command swarm-relay is the public relay for the Swarm tunnel. It reads
// RELAY_SECRET (at least 64 hex characters) and PORT, and logs no secrets.
package main

import (
	"errors"
	"log"
	"net/http"
	"os"

	"github.com/AlexanderTar/agent-swarm/internal/relay"
)

func main() {
	secret := os.Getenv("RELAY_SECRET")
	if !relay.ValidSecret(secret) {
		log.Fatal("swarm-relay: RELAY_SECRET must be at least 64 hex characters (openssl rand -hex 32)")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := relay.NewServer(":"+port, relay.New(secret, relay.Options{}))
	log.Printf("swarm-relay: listening on :%s", port)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
