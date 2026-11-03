package main

import (
	"sync"
	"time"
)

// EventStore is a thread-safe in-memory store for campaign events.
type EventStore struct {
	mu sync.RWMutex

	// events stores all accepted events, keyed by campaign_id.
	events map[string][]Event

	// seen tracks event_ids we have already processed for global dedup.
	seen map[string]bool

	// stats stores pre-computed campaign statistics.
	stats map[string]*campaignStats

	// lastUpdated tracks the last time each campaign was updated.
	lastUpdated map[string]time.Time
}

// campaignStats holds the mutable counters for a campaign.
type campaignStats struct {
	sent        int
	delivered   int
	opened      int
	clicked     int
	uniqueOpens int
	openedBy    map[string]bool // tracks contact_ids that have opened
}

// NewEventStore creates a new empty EventStore.
func NewEventStore() *EventStore {
	return &EventStore{
		events:      make(map[string][]Event),
		seen:        make(map[string]bool),
		stats:       make(map[string]*campaignStats),
		lastUpdated: make(map[string]time.Time),
	}
}

// AddEvent attempts to add an event to the store.
// Returns:
//   - "accepted" if the event was new and stored
//   - "duplicate" if the event_id was already seen
func (s *EventStore) AddEvent(ev Event) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Global dedup by event_id
	if s.seen[ev.EventID] {
		return "duplicate"
	}
	s.seen[ev.EventID] = true

	// Ensure campaign stats exist
	cs, ok := s.stats[ev.CampaignID]
	if !ok {
		cs = &campaignStats{
			openedBy: make(map[string]bool),
		}
		s.stats[ev.CampaignID] = cs
	}

	// Update counters
	switch ev.Type {
	case "sent":
		cs.sent++
	case "delivered":
		cs.delivered++
	case "opened":
		cs.opened++
		// Track unique opens per campaign
		if !cs.openedBy[ev.ContactID] {
			cs.openedBy[ev.ContactID] = true
			cs.uniqueOpens++
		}
	case "clicked":
		cs.clicked++
	}

	// Store the event
	s.events[ev.CampaignID] = append(s.events[ev.CampaignID], ev)

	// Update timestamp
	s.lastUpdated[ev.CampaignID] = time.Now().UTC()

	return "accepted"
}

// GetStats returns the aggregated stats for a campaign.
// Returns nil if the campaign does not exist.
func (s *EventStore) GetStats(campaignID string) *StatsResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cs, ok := s.stats[campaignID]
	if !ok {
		return nil
	}

	lastUpdated := s.lastUpdated[campaignID]
	return &StatsResponse{
		CampaignID: campaignID,
		Stats: CampaignStat{
			Sent:        cs.sent,
			Delivered:   cs.delivered,
			Opened:      cs.opened,
			Clicked:     cs.clicked,
			UniqueOpens: cs.uniqueOpens,
		},
		LastUpdated: &lastUpdated,
	}
}

// GetEvents returns a paginated list of events for a campaign.
// Returns nil if the campaign does not exist.
func (s *EventStore) GetEvents(campaignID string, offset, limit int) *EventsResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	events, ok := s.events[campaignID]
	if !ok {
		return nil
	}

	total := len(events)

	// Clamp offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}

	// Clamp limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	end := offset + limit
	if end > total {
		end = total
	}

	// Build output slice
	outputEvents := make([]EventOutput, 0, end-offset)
	for _, ev := range events[offset:end] {
		outputEvents = append(outputEvents, eventToOutput(ev))
	}

	return &EventsResponse{
		CampaignID: campaignID,
		Events:     outputEvents,
		Pagination: PaginationInfo{
			Total:   total,
			Limit:   limit,
			Offset:  offset,
			HasMore: end < total,
		},
	}
}
