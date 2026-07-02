package domain

import "sort"

// Nudge types (proactive waiter prompts derived from the per-table timers).
const (
	NudgeGreet    = "greet"
	NudgeCheckin  = "checkin"
	NudgeAnything = "anything"
)

// Human labels surfaced on the waiter floor map (match the Node demo wording).
var nudgeLabels = map[string]string{
	NudgeGreet:    "Greet the guests",
	NudgeCheckin:  "Ask how the food is",
	NudgeAnything: "Check if they need anything",
}

// NudgeConfig is the effective nudge timing, resolved from SettingsService
// (floor.nudge.*). Delays are in SECONDS; the engine compares against epoch-ms
// timers. A disabled type never fires.
type NudgeConfig struct {
	GreetEnabled       bool
	GreetDelaySecs     int64 // floor.nudge.greet_secs

	CheckinEnabled     bool
	CheckinAfterServeSecs int64 // floor.nudge.checkin_secs

	AnythingEnabled    bool
	AnythingAfterCheckinSecs int64 // floor.nudge.anything_secs
}

// DefaultNudgeConfig mirrors the Node defaults (greet 30s, check-in 300s after a
// serve, anything 600s after a check-in). Used when settings omits a key.
func DefaultNudgeConfig() NudgeConfig {
	return NudgeConfig{
		GreetEnabled: true, GreetDelaySecs: 30,
		CheckinEnabled: true, CheckinAfterServeSecs: 300,
		AnythingEnabled: true, AnythingAfterCheckinSecs: 600,
	}
}

// Nudge is one prompt for one table.
type Nudge struct {
	Table int32
	Type  string
	Label string
	Since int64 // epoch ms the timer started (greet=seatedAt, checkin=lastServedAt, anything=lastCheckinAt)
}

// nudgeForTable returns the single most urgent nudge for a table, or false.
// Priority: greet > checkin > anything. Ported verbatim from the Node engine so
// the proven suppression rules hold:
//   - greet:    seated, not yet greeted, greet delay elapsed.
//   - checkin:  served, no check-in since that serve, after-serve delay elapsed.
//   - anything: checked in, after-check-in delay elapsed, AND no newer serve is
//     pending (a serve after the last check-in resets us to the check-in track).
func nudgeForTable(t Table, nowMs int64, cfg NudgeConfig) (Nudge, bool) {
	// greet
	if cfg.GreetEnabled &&
		t.SeatedAt != 0 &&
		t.GreetedAt == 0 &&
		nowMs-t.SeatedAt >= cfg.GreetDelaySecs*1000 {
		return Nudge{Table: t.N, Type: NudgeGreet, Label: nudgeLabels[NudgeGreet], Since: t.SeatedAt}, true
	}

	// checkin — fires after a serve when no check-in has happened since that serve.
	if cfg.CheckinEnabled &&
		t.LastServedAt != 0 &&
		(t.LastCheckinAt == 0 || t.LastCheckinAt < t.LastServedAt) &&
		nowMs-t.LastServedAt >= cfg.CheckinAfterServeSecs*1000 {
		return Nudge{Table: t.N, Type: NudgeCheckin, Label: nudgeLabels[NudgeCheckin], Since: t.LastServedAt}, true
	}

	// anything — after a check-in, unless a newer serve is pending (back to checkin).
	if cfg.AnythingEnabled &&
		t.LastCheckinAt != 0 &&
		nowMs-t.LastCheckinAt >= cfg.AnythingAfterCheckinSecs*1000 &&
		!(t.LastServedAt != 0 && t.LastServedAt > t.LastCheckinAt) {
		return Nudge{Table: t.N, Type: NudgeAnything, Label: nudgeLabels[NudgeAnything], Since: t.LastCheckinAt}, true
	}

	return Nudge{}, false
}

// BuildNudges returns the active nudges across all tables, oldest-since first
// (the table waiting longest is surfaced at the top). One nudge per table max.
func BuildNudges(tables []Table, nowMs int64, cfg NudgeConfig) []Nudge {
	out := make([]Nudge, 0, len(tables))
	for _, t := range tables {
		if n, ok := nudgeForTable(t, nowMs, cfg); ok {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Since == out[j].Since {
			return out[i].Table < out[j].Table
		}
		return out[i].Since < out[j].Since
	})
	return out
}
