# AI Usage Disclosure

## Tools Used
- Claude (Anthropic) via Antigravity IDE

## What I Used AI For
- Scaffolding the initial project structure and boilerplate
- Identifying the 4 bugs in the debugging exercise (confirmed each by reading the code and running with -race)
- Drafting the scale memo — I know the concepts but AI helped structure the answer
- Reviewing edge cases in the seed data file

## A Suggestion I Rejected or Changed
AI initially suggested using `sync/atomic` for all counter increments in the debugging fix. I changed this to use a `sync.Mutex` on the `CampaignStats` struct instead, because:
1. The spec says "make minimal changes" and a mutex is a simpler, more contained fix
2. Atomic operations on multiple fields don't give you transactional consistency across them
3. The mutex approach is more readable and maintainable

## Something AI Helped Me Understand
The nuance of per-item JSON parsing with `json.RawMessage` — specifically, that decoding a JSON array into `[]json.RawMessage` lets you handle each element independently, so one malformed element doesn't prevent the rest from being processed. This is important for a webhook endpoint where you want to be as accepting as possible.

## Things AI Generated That I Had to Debug
The initial test for concurrent POST requests had a subtle issue where goroutines were sharing a loop variable. I had to fix the closure capture to avoid a data race in the test itself.
