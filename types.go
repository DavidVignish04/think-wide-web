package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Event is one webhook event from a message provider.
type Event struct {
	EventID    string            `json:"event_id"`
	CampaignID string            `json:"campaign_id"`
	ContactID  string            `json:"contact_id"`
	Type       string            `json:"type"`
	Timestamp  time.Time         `json:"timestamp"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// validEventTypes is the set of accepted event types.
var validEventTypes = map[string]bool{
	"sent":      true,
	"delivered": true,
	"opened":    true,
	"clicked":   true,
}

// Validate checks that an event has all required fields and valid values.
// Returns an error string if invalid, or empty string if valid.
func (e *Event) Validate() string {
	if strings.TrimSpace(e.EventID) == "" {
		return "missing event_id"
	}
	if strings.TrimSpace(e.CampaignID) == "" {
		return "missing campaign_id"
	}
	if strings.TrimSpace(e.ContactID) == "" {
		return "missing contact_id"
	}
	if !validEventTypes[e.Type] {
		return fmt.Sprintf("invalid event type: %q", e.Type)
	}
	if e.Timestamp.IsZero() {
		return "invalid or missing timestamp"
	}
	return ""
}

// RawEventItem is used for per-item JSON parsing.
// We unmarshal the array into []json.RawMessage first, then parse each item.
type RawEventItem struct {
	EventID    string            `json:"event_id"`
	CampaignID string            `json:"campaign_id"`
	ContactID  string            `json:"contact_id"`
	Type       string            `json:"type"`
	Timestamp  string            `json:"timestamp"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ToEvent converts a RawEventItem to an Event, parsing the timestamp.
// Returns the Event and an error string if parsing failed.
func (r *RawEventItem) ToEvent() (Event, string) {
	var ev Event
	ev.EventID = r.EventID
	ev.CampaignID = r.CampaignID
	ev.ContactID = r.ContactID
	ev.Type = r.Type
	ev.Metadata = r.Metadata

	if strings.TrimSpace(r.Timestamp) == "" {
		return ev, "missing timestamp"
	}
	t, err := time.Parse(time.RFC3339, r.Timestamp)
	if err != nil {
		return ev, fmt.Sprintf("invalid timestamp: %s", r.Timestamp)
	}
	ev.Timestamp = t

	return ev, ""
}

// --- Response types ---

// IngestResponse is returned by POST /events.
type IngestResponse struct {
	Accepted   int           `json:"accepted"`
	Duplicates int           `json:"duplicates"`
	Rejected   int           `json:"rejected"`
	Errors     []IngestError `json:"errors,omitempty"`
}

// IngestError describes why a single event in a batch was rejected.
type IngestError struct {
	Index   int    `json:"index"`
	EventID string `json:"event_id,omitempty"`
	Reason  string `json:"reason"`
}

// StatsResponse is returned by GET /campaigns/{campaign_id}/stats.
type StatsResponse struct {
	CampaignID  string       `json:"campaign_id"`
	Stats       CampaignStat `json:"stats"`
	LastUpdated *time.Time   `json:"last_updated,omitempty"`
}

// CampaignStat holds the aggregated counts for a campaign.
type CampaignStat struct {
	Sent        int `json:"sent"`
	Delivered   int `json:"delivered"`
	Opened      int `json:"opened"`
	Clicked     int `json:"clicked"`
	UniqueOpens int `json:"unique_opens"`
}

// EventsResponse is returned by GET /campaigns/{campaign_id}/events.
type EventsResponse struct {
	CampaignID string          `json:"campaign_id"`
	Events     []EventOutput   `json:"events"`
	Pagination PaginationInfo  `json:"pagination"`
}

// EventOutput is a single event in the events list response.
type EventOutput struct {
	EventID    string            `json:"event_id"`
	CampaignID string            `json:"campaign_id"`
	ContactID  string            `json:"contact_id"`
	Type       string            `json:"type"`
	Timestamp  time.Time         `json:"timestamp"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// PaginationInfo describes the pagination state.
type PaginationInfo struct {
	Total   int  `json:"total"`
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	HasMore bool `json:"has_more"`
}

// ErrorResponse is a generic error returned by the API.
type ErrorResponse struct {
	Error string `json:"error"`
}

// eventToOutput converts an Event to an EventOutput for API responses.
func eventToOutput(ev Event) EventOutput {
	return EventOutput{
		EventID:    ev.EventID,
		CampaignID: ev.CampaignID,
		ContactID:  ev.ContactID,
		Type:       ev.Type,
		Timestamp:  ev.Timestamp,
		Metadata:   ev.Metadata,
	}
}


