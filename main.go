package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	store = NewEventStore()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /events", handlePostEvents)
	mux.HandleFunc("GET /campaigns/{campaignID}/stats", handleGetStats)
	mux.HandleFunc("GET /campaigns/{campaignID}/events", handleGetEvents)

	addr := ":8080"
	fmt.Printf("Campaign Events service listening on %s\n", addr)
	fmt.Println("Endpoints:")
	fmt.Println("  POST /events                          - Ingest events")
	fmt.Println("  GET  /campaigns/{campaignID}/stats     - Campaign statistics")
	fmt.Println("  GET  /campaigns/{campaignID}/events    - Paginated event list")
	log.Fatal(http.ListenAndServe(addr, mux))
}
