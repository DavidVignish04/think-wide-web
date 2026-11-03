# Part 3 — Debugging Findings & Fixes (`BUGS.md`)

This document records the four bugs identified and fixed in `debugging/main.go`.

---

## Bug 1 — Data Race on Counter Increments

### 1. What the bug is
Concurrent writes without synchronization to the counter fields (`Sent`, `Delivered`, `Opened`, `Clicked`) of `CampaignStats` structs inside `apply()`.

### 2. Why it happens
In `processBatch()`, up to 8 worker goroutines are spawned to consume events from the `jobs` channel and execute `apply(ev)`. When multiple events for the same `campaign_id` are pulled concurrently by different workers, multiple goroutines execute non-atomic read-modify-write operations (`cs.Sent++`, `cs.Delivered++`, etc.) on the shared `*CampaignStats` pointer simultaneously. Plain integer increments are not atomic in Go, leading to data races, memory corruption, and lost counts under concurrency.

### 3. Minimal Fix
Add a `sync.Mutex` field to `CampaignStats` and acquire/release it within `apply()`.

```diff
--- a/debugging/main.go
+++ b/debugging/main.go
@@ -31,6 +31,7 @@
 type CampaignStats struct {
+	mu             sync.Mutex
 	Sent           int
 	Delivered      int
 	Opened         int
 	Clicked        int
 	UniqueOpens    int
 	DailyDelivered map[string]int
 }
@@ -151,6 +152,8 @@
 func apply(ev Event) {
 	cs := stats[ev.CampaignID]
+	cs.mu.Lock()
+	defer cs.mu.Unlock()
 	switch ev.Type {
 	case "sent":
 		cs.Sent++
 	case "delivered":
 		cs.Delivered++
 	case "opened":
 		cs.Opened++
 	case "clicked":
 		cs.Clicked++
 	}
 }
```

### 4. How it was verified
Run with the Go race detector:
```bash
go run -race . events.jsonl
```
Before the fix, the race detector reports data races on writes to `cs.Sent`, `cs.Delivered`, `cs.Opened`, and `cs.Clicked`. After adding `cs.mu.Lock()`/`defer cs.mu.Unlock()`, the race detector reports clean execution with zero races detected.

---

## Bug 2 — `openedBy` Tracks Contacts Globally Rather Than Per-Campaign

### 1. What the bug is
The deduplication map for unique opens (`openedBy`) used only `ev.ContactID` as its lookup key, deduplicating opens globally across the entire program rather than per campaign.

### 2. Why it happens
Requirement 2 specifies: "`unique_opens` = the number of distinct contacts that opened, per campaign. One contact opening five times in a campaign counts once. The same contact opening in two campaigns counts once *in each*."
Because `openedBy` was keyed solely on `contactID`, once contact `ct_123` opened an email in campaign A, subsequent opens by `ct_123` in campaign B were filtered out by `if !openedBy[ev.ContactID]`, causing undercounting of `unique_opens` in subsequent campaigns.

### 3. Minimal Fix
Scope the deduplication key to each campaign by combining `campaignID` and `contactID`: `ev.CampaignID + "|" + ev.ContactID`.

```diff
--- a/debugging/main.go
+++ b/debugging/main.go
@@ -138,8 +138,9 @@
 	case "opened":
-		if !openedBy[ev.ContactID] {
-			openedBy[ev.ContactID] = true
+		key := ev.CampaignID + "|" + ev.ContactID
+		if !openedBy[key] {
+			openedBy[key] = true
 			cs.UniqueOpens++
 		}
```

### 4. How it was verified
Comparing output against `expected_output.txt`:
Without this fix, `cmp_B` showed 829 instead of 833, `cmp_C` showed 702 instead of 710, `cmp_D` showed 507 instead of 514, and `cmp_E` showed 377 instead of 380. With the compound key `campaignID + "|" + contactID`, all `unique_opens` values match `expected_output.txt` exactly.

---

## Bug 3 — Daily Delivery Buckets Use Local Timezone Instead of UTC

### 1. What the bug is
Line 144 formatted timestamps with `ev.Timestamp.Local().Format("2006-01-02")`, grouping delivered events by their local calendar date rather than UTC date.

### 2. Why it happens
Requirement 3 states: "Daily `delivered` buckets use the event's UTC date. The `timestamp` field is UTC." Calling `.Local()` converts the timestamp to whatever local timezone the host system is configured for. On any system whose timezone is not UTC (e.g. UTC+05:30 or UTC-07:00), events occurring near midnight UTC shift into the previous or following calendar date, producing incorrect daily delivery totals.

### 3. Minimal Fix
Change `.Local()` to `.UTC()`.

```diff
--- a/debugging/main.go
+++ b/debugging/main.go
@@ -143,3 +144,3 @@
 	case "delivered":
-		day := ev.Timestamp.Local().Format("2006-01-02")
+		day := ev.Timestamp.UTC().Format("2006-01-02")
 		cs.DailyDelivered[day]++
```

### 4. How it was verified
On any non-UTC environment, running the original code produced bucket counts that deviated from `expected_output.txt` (or produced dates outside the expected August 1–7 range). With `.UTC()`, daily delivered buckets match `expected_output.txt` across all campaigns regardless of machine timezone.

---

## Bug 4 — Deduplication Is Per-Batch Instead of Global Across the Entire File

### 1. What the bug is
The `seen` map for deduplicating `event_id` was allocated as a local variable inside `processBatch()` on each invocation (`seen := make(map[string]bool)`).

### 2. Why it happens
Requirement 1 states: "Count each `event_id` at most once across the whole file. Providers retry; the input contains duplicate lines. A duplicate must not change any number."
Events are ingested in batches of 200 (`batchSize = 200`). Creating a new `seen` map per batch only deduplicates events within the same 200-event batch. When duplicate events (such as provider retries) span across batch boundaries (e.g. batch 1 and batch 3), the subsequent occurrences are not recognized as duplicates and are processed again, inflating all counters.

### 3. Minimal Fix
Promote `seen` to package-level scope alongside `openedBy` so that seen `event_id`s persist across all batches for the lifetime of the process.

```diff
--- a/debugging/main.go
+++ b/debugging/main.go
@@ -48,2 +48,5 @@
 var openedBy = map[string]bool{}
 
+// seen tracks event IDs that have already been processed across all batches.
+var seen = map[string]bool{}
+
@@ -100,3 +103,2 @@
 func processBatch(events []Event) {
-	seen := make(map[string]bool) // event IDs we have already processed
 	unique := make([]Event, 0, len(events))
```

### 4. How it was verified
Comparing program output against `expected_output.txt`:
Before the fix, total counts across campaigns were inflated due to cross-batch duplicates. After making `seen` package-level, all counts (`sent`, `delivered`, `opened`, `clicked`) match `expected_output.txt` exactly, and `diff actual.txt expected_output.txt` is completely empty.
