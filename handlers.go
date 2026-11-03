package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
)

// store is the global event store instance, initialised in main().
var store *EventStore

// handlePostEvents ingests a JSON array of events from providers.
// Each item is parsed individually so one bad event doesn't fail the batch.
func handlePostEvents(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, ErrorResponse{Error: "failed to read request body"})
		return
	}
	defer r.Body.Close()

	// Parse the outer array into raw messages
	var rawItems []json.RawMessage
	if err := json.Unmarshal(body, &rawItems); err != nil {
		respondJSON(w, http.StatusBadRequest, ErrorResponse{Error: "request body must be a JSON array"})
		return
	}

	response := IngestResponse{
		Errors: make([]IngestError, 0),
	}

	for i, raw := range rawItems {
		// Parse each item individually
		var rawEvent RawEventItem
		if err := json.Unmarshal(raw, &rawEvent); err != nil {
			response.Rejected++
			response.Errors = append(response.Errors, IngestError{
				Index:  i,
				Reason: "malformed JSON: " + err.Error(),
			})
			continue
		}

		// Convert to Event (parses timestamp)
		ev, parseErr := rawEvent.ToEvent()
		if parseErr != "" {
			response.Rejected++
			response.Errors = append(response.Errors, IngestError{
				Index:   i,
				EventID: rawEvent.EventID,
				Reason:  parseErr,
			})
			continue
		}

		// Validate required fields and types
		if validationErr := ev.Validate(); validationErr != "" {
			response.Rejected++
			response.Errors = append(response.Errors, IngestError{
				Index:   i,
				EventID: ev.EventID,
				Reason:  validationErr,
			})
			continue
		}

		// Attempt to store
		result := store.AddEvent(ev)
		switch result {
		case "accepted":
			response.Accepted++
		case "duplicate":
			response.Duplicates++
		}
	}

	respondJSON(w, http.StatusOK, response)
}

// handleGetStats returns aggregated stats for one campaign.
func handleGetStats(w http.ResponseWriter, r *http.Request) {
	campaignID := r.PathValue("campaignID")

	stats := store.GetStats(campaignID)
	if stats == nil {
		respondJSON(w, http.StatusNotFound, ErrorResponse{Error: "campaign not found"})
		return
	}

	respondJSON(w, http.StatusOK, stats)
}

// handleGetEvents returns a paginated list of events for one campaign.
func handleGetEvents(w http.ResponseWriter, r *http.Request) {
	campaignID := r.PathValue("campaignID")

	// Parse pagination params
	offsetStr := r.URL.Query().Get("offset")
	limitStr := r.URL.Query().Get("limit")

	offset := 0
	limit := 20

	if offsetStr != "" {
		if v, err := strconv.Atoi(offsetStr); err == nil && v >= 0 {
			offset = v
		}
	}
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			limit = v
		}
	}

	events := store.GetEvents(campaignID, offset, limit)
	if events == nil {
		respondJSON(w, http.StatusNotFound, ErrorResponse{Error: "campaign not found"})
		return
	}

	respondJSON(w, http.StatusOK, events)
}

// respondJSON encodes v as JSON and writes it with the given status code.
func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("respondJSON: %v", err)
	}
}
