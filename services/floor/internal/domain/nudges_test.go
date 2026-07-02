package domain_test

import (
	"testing"

	"github.com/restorna/platform/services/floor/internal/domain"
)

const now int64 = 1_000_000_000_000 // fixed epoch ms

func cfg() domain.NudgeConfig { return domain.DefaultNudgeConfig() }

// tbl builds a table with the four nudge timers, defaulting unset.
func tbl(over domain.Table) domain.Table {
	if over.N == 0 {
		over.N = 1
	}
	return over
}

func TestNudge_GreetAfterDelay(t *testing.T) {
	// 29s seated: not yet (greet delay is 30s).
	before := domain.BuildNudges([]domain.Table{tbl(domain.Table{SeatedAt: now - 29_000})}, now, cfg())
	if len(before) != 0 {
		t.Fatalf("greet fired too early: %+v", before)
	}
	// 30s seated: fires.
	after := domain.BuildNudges([]domain.Table{tbl(domain.Table{SeatedAt: now - 30_000})}, now, cfg())
	if len(after) != 1 || after[0].Type != domain.NudgeGreet {
		t.Fatalf("greet should fire at 30s: %+v", after)
	}
	if after[0].Label != "Greet the guests" || after[0].Since != now-30_000 {
		t.Fatalf("greet payload wrong: %+v", after[0])
	}
}

func TestNudge_GreetSuppressedAfterAck(t *testing.T) {
	res := domain.BuildNudges([]domain.Table{tbl(domain.Table{SeatedAt: now - 60_000, GreetedAt: now - 10_000})}, now, cfg())
	if len(res) != 0 {
		t.Fatalf("greet should be suppressed once greetedAt set: %+v", res)
	}
}

func TestNudge_CheckinAfterServe(t *testing.T) {
	base := domain.Table{SeatedAt: now - 9_999_999, GreetedAt: now - 9_999_999}
	// 299s since serve: not yet (check-in is 300s after serve).
	b := base
	b.LastServedAt = now - 299_000
	if got := domain.BuildNudges([]domain.Table{tbl(b)}, now, cfg()); len(got) != 0 {
		t.Fatalf("checkin fired too early: %+v", got)
	}
	// 300s: fires.
	a := base
	a.LastServedAt = now - 300_000
	got := domain.BuildNudges([]domain.Table{tbl(a)}, now, cfg())
	if len(got) != 1 || got[0].Type != domain.NudgeCheckin {
		t.Fatalf("checkin should fire at 300s: %+v", got)
	}
	if got[0].Since != now-300_000 {
		t.Fatalf("checkin since wrong: %+v", got[0])
	}
}

func TestNudge_AnythingAfterCheckin(t *testing.T) {
	res := domain.BuildNudges([]domain.Table{tbl(domain.Table{
		SeatedAt: now - 9_999_999, GreetedAt: now - 9_999_999,
		LastCheckinAt: now - 600_000,
	})}, now, cfg())
	if len(res) != 1 || res[0].Type != domain.NudgeAnything {
		t.Fatalf("anything should fire at 600s after checkin: %+v", res)
	}
}

func TestNudge_AnythingSuppressedWhenNewerServePending(t *testing.T) {
	// Checked in long ago, but a NEWER serve is pending and not yet past the
	// after-serve delay: anything is suppressed and checkin not yet due.
	res := domain.BuildNudges([]domain.Table{tbl(domain.Table{
		SeatedAt: now - 9_999_999, GreetedAt: now - 9_999_999,
		LastCheckinAt: now - 700_000,
		LastServedAt:  now - 100_000,
	})}, now, cfg())
	if len(res) != 0 {
		t.Fatalf("anything should be suppressed by a newer pending serve: %+v", res)
	}
}

func TestNudge_CheckinRefiresAfterNewServe(t *testing.T) {
	// Checked in, then a newer serve happened > afterServeSecs ago -> checkin again.
	res := domain.BuildNudges([]domain.Table{tbl(domain.Table{
		SeatedAt: now - 9_999_999, GreetedAt: now - 9_999_999,
		LastCheckinAt: now - 400_000,
		LastServedAt:  now - 350_000,
	})}, now, cfg())
	if len(res) != 1 || res[0].Type != domain.NudgeCheckin || res[0].Since != now-350_000 {
		t.Fatalf("checkin should re-fire after a new serve: %+v", res)
	}
}

func TestNudge_GreetPriority(t *testing.T) {
	res := domain.BuildNudges([]domain.Table{tbl(domain.Table{
		SeatedAt:      now - 60_000,
		LastServedAt:  now - 400_000,
		LastCheckinAt: now - 700_000,
	})}, now, cfg())
	if len(res) != 1 || res[0].Type != domain.NudgeGreet {
		t.Fatalf("greet should win over checkin/anything: %+v", res)
	}
}

func TestNudge_DisabledNeverFire(t *testing.T) {
	c := cfg()
	c.GreetEnabled, c.CheckinEnabled, c.AnythingEnabled = false, false, false
	res := domain.BuildNudges([]domain.Table{tbl(domain.Table{
		SeatedAt: now - 60_000, LastServedAt: now - 400_000, LastCheckinAt: now - 700_000,
	})}, now, c)
	if len(res) != 0 {
		t.Fatalf("disabled types must not fire: %+v", res)
	}
}

func TestNudge_SortedByOldestSince(t *testing.T) {
	res := domain.BuildNudges([]domain.Table{
		tbl(domain.Table{N: 5, SeatedAt: now - 40_000}),
		tbl(domain.Table{N: 9, SeatedAt: now - 90_000}),
	}, now, cfg())
	if len(res) != 2 {
		t.Fatalf("want 2 nudges, got %d", len(res))
	}
	if res[0].Table != 9 || res[1].Table != 5 {
		t.Fatalf("oldest-since should sort first: %+v", res)
	}
}
