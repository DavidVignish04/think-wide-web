package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// setupTestStore initialises a fresh store for each test.
func setupTestStore() {
	store = NewEventStore()
}

// postEvents is a test helper that sends a POST /events request.
func postEvents(t *testing.T, handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// getStats is a test helper that sends a GET /campaigns/{id}/stats request.
func getStats(t *testing.T, mux *http.ServeMux, campaignID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/campaigns/"+campaignID+"/stats", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// getEvents is a test helper that sends a GET /campaigns/{id}/events request.
func getEvents(t *testing.T, mux *http.ServeMux, campaignID string, query string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/campaigns/" + campaignID + "/events"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /events", handlePostEvents)
	mux.HandleFunc("GET /campaigns/{campaignID}/stats", handleGetStats)
	mux.HandleFunc("GET /campaigns/{campaignID}/events", handleGetEvents)
	return mux
}

func TestHappyPath(t *testing.T) {
	setupTestStore()
	mux := newMux()

	body := `[
		{"event_id":"evt_1","campaign_id":"cmp_1","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:01:00Z"},
		{"event_id":"evt_2","campaign_id":"cmp_1","contact_id":"ct_1","type":"delivered","timestamp":"2026-08-10T06:30:00Z"},
		{"event_id":"evt_3","campaign_id":"cmp_1","contact_id":"ct_1","type":"opened","timestamp":"2026-08-10T09:00:00Z"},
		{"event_id":"evt_4","campaign_id":"cmp_1","contact_id":"ct_1","type":"clicked","timestamp":"2026-08-10T09:05:00Z","metadata":{"url":"https://example.com"}}
	]`

	w := postEvents(t, handlePostEvents, body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp IngestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Accepted != 4 {
		t.Errorf("expected 4 accepted, got %d", resp.Accepted)
	}
	if resp.Duplicates != 0 {
		t.Errorf("expected 0 duplicates, got %d", resp.Duplicates)
	}
	if resp.Rejected != 0 {
		t.Errorf("expected 0 rejected, got %d", resp.Rejected)
	}

	// Check stats
	sw := getStats(t, mux, "cmp_1")
	if sw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", sw.Code)
	}
	var statsResp StatsResponse
	if err := json.Unmarshal(sw.Body.Bytes(), &statsResp); err != nil {
		t.Fatalf("failed to parse stats: %v", err)
	}
	if statsResp.Stats.Sent != 1 {
		t.Errorf("sent: expected 1, got %d", statsResp.Stats.Sent)
	}
	if statsResp.Stats.Delivered != 1 {
		t.Errorf("delivered: expected 1, got %d", statsResp.Stats.Delivered)
	}
	if statsResp.Stats.Opened != 1 {
		t.Errorf("opened: expected 1, got %d", statsResp.Stats.Opened)
	}
	if statsResp.Stats.Clicked != 1 {
		t.Errorf("clicked: expected 1, got %d", statsResp.Stats.Clicked)
	}
	if statsResp.Stats.UniqueOpens != 1 {
		t.Errorf("unique_opens: expected 1, got %d", statsResp.Stats.UniqueOpens)
	}
}

func TestDeduplication(t *testing.T) {
	setupTestStore()

	body := `[
		{"event_id":"evt_1","campaign_id":"cmp_1","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:01:00Z"},
		{"event_id":"evt_1","campaign_id":"cmp_1","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:01:00Z"}
	]`

	w := postEvents(t, handlePostEvents, body)
	var resp IngestResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Accepted != 1 {
		t.Errorf("expected 1 accepted, got %d", resp.Accepted)
	}
	if resp.Duplicates != 1 {
		t.Errorf("expected 1 duplicate, got %d", resp.Duplicates)
	}

	// Stats should show only 1 sent
	stats := store.GetStats("cmp_1")
	if stats.Stats.Sent != 1 {
		t.Errorf("sent after dedup: expected 1, got %d", stats.Stats.Sent)
	}
}

func TestDeduplicationAcrossBatches(t *testing.T) {
	setupTestStore()

	body1 := `[{"event_id":"evt_1","campaign_id":"cmp_1","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:01:00Z"}]`
	body2 := `[{"event_id":"evt_1","campaign_id":"cmp_1","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:01:00Z"}]`

	postEvents(t, handlePostEvents, body1)
	w := postEvents(t, handlePostEvents, body2)

	var resp IngestResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Duplicates != 1 {
		t.Errorf("cross-batch dedup: expected 1 duplicate, got %d", resp.Duplicates)
	}

	stats := store.GetStats("cmp_1")
	if stats.Stats.Sent != 1 {
		t.Errorf("sent after cross-batch dedup: expected 1, got %d", stats.Stats.Sent)
	}
}

func TestSameEventIDDifferentType(t *testing.T) {
	setupTestStore()

	// Same event_id, different types — first one wins
	body := `[
		{"event_id":"evt_1","campaign_id":"cmp_1","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:01:00Z"},
		{"event_id":"evt_1","campaign_id":"cmp_1","contact_id":"ct_1","type":"clicked","timestamp":"2026-08-10T06:10:00Z"}
	]`

	w := postEvents(t, handlePostEvents, body)
	var resp IngestResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Accepted != 1 {
		t.Errorf("expected 1 accepted, got %d", resp.Accepted)
	}
	if resp.Duplicates != 1 {
		t.Errorf("expected 1 duplicate, got %d", resp.Duplicates)
	}

	stats := store.GetStats("cmp_1")
	if stats.Stats.Sent != 1 {
		t.Errorf("sent: expected 1, got %d", stats.Stats.Sent)
	}
	if stats.Stats.Clicked != 0 {
		t.Errorf("clicked: expected 0 (deduped), got %d", stats.Stats.Clicked)
	}
}

func TestInvalidEvents(t *testing.T) {
	setupTestStore()

	body := `[
		{"event_id":"","campaign_id":"cmp_1","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:01:00Z"},
		{"event_id":"evt_2","campaign_id":"","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:01:00Z"},
		{"event_id":"evt_3","campaign_id":"cmp_1","contact_id":"","type":"sent","timestamp":"2026-08-10T06:01:00Z"},
		{"event_id":"evt_4","campaign_id":"cmp_1","contact_id":"ct_1","type":"spam_report","timestamp":"2026-08-10T06:01:00Z"},
		{"event_id":"evt_5","campaign_id":"cmp_1","contact_id":"ct_1","type":"sent","timestamp":"not-a-time"},
		{"event_id":"evt_6","campaign_id":"cmp_1","contact_id":"ct_1","type":"OPENED","timestamp":"2026-08-10T06:01:00Z"}
	]`

	w := postEvents(t, handlePostEvents, body)
	var resp IngestResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Rejected != 6 {
		t.Errorf("expected 6 rejected, got %d", resp.Rejected)
	}
	if resp.Accepted != 0 {
		t.Errorf("expected 0 accepted, got %d", resp.Accepted)
	}
	if len(resp.Errors) != 6 {
		t.Errorf("expected 6 errors, got %d", len(resp.Errors))
	}
}

func TestMixedBatch(t *testing.T) {
	setupTestStore()

	body := `[
		{"event_id":"evt_1","campaign_id":"cmp_1","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:01:00Z"},
		{"event_id":"","campaign_id":"cmp_1","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:01:00Z"},
		{"event_id":"evt_3","campaign_id":"cmp_1","contact_id":"ct_1","type":"delivered","timestamp":"2026-08-10T06:30:00Z"},
		{"this is broken json"
	]`

	// Note: the last item is malformed JSON within the array, but the array itself
	// won't parse. Let's use a valid array with a mix instead.
	body = `[
		{"event_id":"evt_1","campaign_id":"cmp_1","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:01:00Z"},
		{"event_id":"","campaign_id":"cmp_1","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:01:00Z"},
		{"event_id":"evt_3","campaign_id":"cmp_1","contact_id":"ct_1","type":"delivered","timestamp":"2026-08-10T06:30:00Z"},
		{"event_id":"evt_4","campaign_id":"cmp_1","contact_id":"ct_1","type":"opened","timestamp":"bad-time"}
	]`

	w := postEvents(t, handlePostEvents, body)
	var resp IngestResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Accepted != 2 {
		t.Errorf("expected 2 accepted, got %d", resp.Accepted)
	}
	if resp.Rejected != 2 {
		t.Errorf("expected 2 rejected, got %d", resp.Rejected)
	}
}

func TestUniqueOpensPerCampaign(t *testing.T) {
	setupTestStore()

	body := `[
		{"event_id":"evt_1","campaign_id":"cmp_1","contact_id":"ct_1","type":"opened","timestamp":"2026-08-10T09:00:00Z"},
		{"event_id":"evt_2","campaign_id":"cmp_1","contact_id":"ct_1","type":"opened","timestamp":"2026-08-10T09:05:00Z"},
		{"event_id":"evt_3","campaign_id":"cmp_1","contact_id":"ct_2","type":"opened","timestamp":"2026-08-10T09:10:00Z"},
		{"event_id":"evt_4","campaign_id":"cmp_2","contact_id":"ct_1","type":"opened","timestamp":"2026-08-10T09:15:00Z"}
	]`

	postEvents(t, handlePostEvents, body)

	// cmp_1: 2 open events, but ct_1 opened twice → unique_opens = 2
	stats1 := store.GetStats("cmp_1")
	if stats1.Stats.Opened != 3 {
		t.Errorf("cmp_1 opened: expected 3, got %d", stats1.Stats.Opened)
	}
	if stats1.Stats.UniqueOpens != 2 {
		t.Errorf("cmp_1 unique_opens: expected 2, got %d", stats1.Stats.UniqueOpens)
	}

	// cmp_2: ct_1 opened (same contact as cmp_1, but separate campaign) → unique_opens = 1
	stats2 := store.GetStats("cmp_2")
	if stats2.Stats.Opened != 1 {
		t.Errorf("cmp_2 opened: expected 1, got %d", stats2.Stats.Opened)
	}
	if stats2.Stats.UniqueOpens != 1 {
		t.Errorf("cmp_2 unique_opens: expected 1, got %d", stats2.Stats.UniqueOpens)
	}
}

func TestStatsNotFound(t *testing.T) {
	setupTestStore()
	mux := newMux()

	w := getStats(t, mux, "cmp_nonexistent")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestPaginatedEvents(t *testing.T) {
	setupTestStore()
	mux := newMux()

	// Ingest 5 events
	body := `[
		{"event_id":"evt_1","campaign_id":"cmp_1","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:01:00Z"},
		{"event_id":"evt_2","campaign_id":"cmp_1","contact_id":"ct_2","type":"sent","timestamp":"2026-08-10T06:02:00Z"},
		{"event_id":"evt_3","campaign_id":"cmp_1","contact_id":"ct_3","type":"sent","timestamp":"2026-08-10T06:03:00Z"},
		{"event_id":"evt_4","campaign_id":"cmp_1","contact_id":"ct_4","type":"sent","timestamp":"2026-08-10T06:04:00Z"},
		{"event_id":"evt_5","campaign_id":"cmp_1","contact_id":"ct_5","type":"sent","timestamp":"2026-08-10T06:05:00Z"}
	]`
	postEvents(t, handlePostEvents, body)

	// Get first page (limit=2)
	w := getEvents(t, mux, "cmp_1", "offset=0&limit=2")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp EventsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Events) != 2 {
		t.Errorf("expected 2 events, got %d", len(resp.Events))
	}
	if resp.Pagination.Total != 5 {
		t.Errorf("expected total 5, got %d", resp.Pagination.Total)
	}
	if !resp.Pagination.HasMore {
		t.Error("expected has_more=true")
	}

	// Get last page
	w2 := getEvents(t, mux, "cmp_1", "offset=4&limit=2")
	var resp2 EventsResponse
	json.Unmarshal(w2.Body.Bytes(), &resp2)
	if len(resp2.Events) != 1 {
		t.Errorf("expected 1 event on last page, got %d", len(resp2.Events))
	}
	if resp2.Pagination.HasMore {
		t.Error("expected has_more=false on last page")
	}
}

func TestEventsNotFound(t *testing.T) {
	setupTestStore()
	mux := newMux()

	w := getEvents(t, mux, "cmp_nonexistent", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestInvalidJSONBody(t *testing.T) {
	setupTestStore()

	w := postEvents(t, handlePostEvents, "not json at all")
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestEmptyArray(t *testing.T) {
	setupTestStore()

	w := postEvents(t, handlePostEvents, "[]")
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for empty array, got %d", w.Code)
	}
	var resp IngestResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Accepted != 0 || resp.Duplicates != 0 || resp.Rejected != 0 {
		t.Errorf("expected all zeros for empty array, got %+v", resp)
	}
}

func TestConcurrentPosts(t *testing.T) {
	setupTestStore()

	// Each goroutine sends a unique event
	var wg sync.WaitGroup
	numGoroutines := 50

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			body := []byte(`[{"event_id":"evt_concurrent_` + strconv.Itoa(idx) + `","campaign_id":"cmp_race","contact_id":"ct_` + strconv.Itoa(idx) + `","type":"sent","timestamp":"2026-08-10T06:01:00Z"}]`)
			req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handlePostEvents(w, req)
		}(i)
	}
	wg.Wait()

	stats := store.GetStats("cmp_race")
	if stats == nil {
		t.Fatal("expected stats for cmp_race")
	}
	if stats.Stats.Sent != numGoroutines {
		t.Errorf("concurrent sent: expected %d, got %d", numGoroutines, stats.Stats.Sent)
	}
}

func TestConcurrentDuplicates(t *testing.T) {
	setupTestStore()

	// All goroutines send the same event_id
	var wg sync.WaitGroup
	numGoroutines := 20

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := `[{"event_id":"evt_same","campaign_id":"cmp_dup","contact_id":"ct_1","type":"delivered","timestamp":"2026-08-10T06:01:00Z"}]`
			req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handlePostEvents(w, req)
		}()
	}
	wg.Wait()

	stats := store.GetStats("cmp_dup")
	if stats == nil {
		t.Fatal("expected stats for cmp_dup")
	}
	if stats.Stats.Delivered != 1 {
		t.Errorf("concurrent dedup: expected 1 delivered, got %d", stats.Stats.Delivered)
	}
}
