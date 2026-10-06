package transferpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestContractJSON(t *testing.T) {
	cfg := Config{
		Paused: true, Timezone: "Europe/Berlin", ConnectionMbps: 100,
		Limit: Limit{Mode: ModePercent, Value: 50}, ScheduleEnabled: true, OutsideSchedule: OutsidePaused,
		Windows: []Window{{
			ID: "night", Name: "Night", Days: []int{0, 6}, Start: "22:00", End: "02:00",
			Action: ActionLimited, Limit: Limit{Mode: ModeKbps, Value: 512},
		}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"paused":true,"timezone":"Europe/Berlin","connectionMbps":100,"limit":{"mode":"percent","value":50},` +
		`"scheduleEnabled":true,"outsideSchedule":"paused","windows":[{"id":"night","name":"Night","days":[0,6],` +
		`"start":"22:00","end":"02:00","action":"limited","limit":{"mode":"kbps","value":512}}]}`
	if string(raw) != want {
		t.Fatalf("Config JSON = %s, want %s", raw, want)
	}
	var roundTrip Config
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roundTrip, cfg) {
		t.Fatalf("Config round trip = %+v, want %+v", roundTrip, cfg)
	}

	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	next := at.Add(2 * time.Hour)
	effective := Effective{Paused: false, Reason: "reason", LimitBytesPerSecond: 1024, NextChange: &next}
	raw, err = json.Marshal(effective)
	if err != nil {
		t.Fatal(err)
	}
	wantEffective := `{"paused":false,"reason":"reason","limitBytesPerSecond":1024,"nextChange":"2026-10-05T14:00:00Z"}`
	if string(raw) != wantEffective {
		t.Fatalf("Effective JSON = %s, want %s", raw, wantEffective)
	}
	raw, err = json.Marshal(Effective{Reason: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"paused":false,"reason":"x","limitBytesPerSecond":0}`; string(raw) != want {
		t.Fatalf("Effective JSON without next change = %s, want %s", raw, want)
	}

	raw, err = json.Marshal(Snapshot{Config: Default(), Effective: Effective{Reason: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	wantSnapshot := `{"config":{"paused":false,"timezone":"UTC","connectionMbps":0,"limit":{"mode":"unlimited","value":0},` +
		`"scheduleEnabled":false,"outsideSchedule":"normal","windows":[]},` +
		`"effective":{"paused":false,"reason":"x","limitBytesPerSecond":0}}`
	if string(raw) != wantSnapshot {
		t.Fatalf("Snapshot JSON = %s, want %s", raw, wantSnapshot)
	}
}

func TestDefaultPolicyIsUnlimited(t *testing.T) {
	cfg := Default()
	if cfg.Timezone != "UTC" || cfg.Paused || cfg.ScheduleEnabled || cfg.OutsideSchedule != OutsideNormal {
		t.Fatalf("Default = %+v", cfg)
	}
	if cfg.Limit.Mode != ModeUnlimited || cfg.ConnectionMbps != 0 || len(cfg.Windows) != 0 {
		t.Fatalf("Default = %+v", cfg)
	}
	eff := Evaluate(cfg, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	if eff.Paused || eff.LimitBytesPerSecond != 0 || eff.NextChange != nil {
		t.Fatalf("Evaluate(Default) = %+v", eff)
	}
	if eff.Reason != "schedule off allows unlimited" {
		t.Fatalf("Evaluate(Default) reason = %q", eff.Reason)
	}
}

func TestEvaluateWindowBoundaries(t *testing.T) {
	cfg := Config{
		Timezone: "UTC", ConnectionMbps: 100, Limit: Limit{Mode: ModeKbps, Value: 1024},
		ScheduleEnabled: true, OutsideSchedule: OutsideNormal,
		Windows: []Window{
			{ID: "morning", Name: "Morning", Days: []int{1}, Start: "06:00", End: "08:00", Action: ActionLimited, Limit: Limit{Mode: ModeKbps, Value: 256}},
			{ID: "night", Name: "Night", Days: []int{0}, Start: "23:00", End: "02:00", Action: ActionPaused},
		},
	}
	base, limited := int64(1024*1024), int64(256*1024)
	monday := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		at     time.Time
		paused bool
		bytes  int64
		reason string
	}{
		{monday.Add(5*time.Hour + 59*time.Minute), false, base, "outside schedule allows 1024 KiB/s"},
		{monday.Add(6 * time.Hour), false, limited, `window "Morning" limits to 256 KiB/s`},
		{monday.Add(7*time.Hour + 59*time.Minute), false, limited, `window "Morning" limits to 256 KiB/s`},
		{monday.Add(8 * time.Hour), false, base, "outside schedule allows 1024 KiB/s"},
		{time.Date(2026, 10, 4, 22, 59, 59, 0, time.UTC), false, base, "outside schedule allows 1024 KiB/s"},
		{time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC), true, 0, `window "Night" is paused`},
		{time.Date(2026, 10, 5, 1, 59, 0, 0, time.UTC), true, 0, `window "Night" is paused`},
		{time.Date(2026, 10, 5, 2, 0, 0, 0, time.UTC), false, base, "outside schedule allows 1024 KiB/s"},
	}
	for _, tc := range cases {
		eff := Evaluate(cfg, tc.at)
		if eff.Paused != tc.paused || eff.LimitBytesPerSecond != tc.bytes || eff.Reason != tc.reason {
			t.Errorf("Evaluate(%s) = %+v", tc.at.Format(time.RFC3339), eff)
		}
	}
	next := Evaluate(cfg, monday.Add(5*time.Hour)).NextChange
	if next == nil || !next.Equal(monday.Add(6*time.Hour)) {
		t.Fatalf("next change from 05:00 = %v, want 06:00", next)
	}
	if next.Location().String() != "UTC" {
		t.Fatalf("next change location = %s, want UTC", next.Location())
	}
	if after := Evaluate(cfg, monday.Add(6*time.Hour)).NextChange; after == nil || !after.Equal(monday.Add(8*time.Hour)) {
		t.Fatalf("next change from 06:00 = %v, want 08:00", after)
	}
}

func TestEvaluateFullDayWindows(t *testing.T) {
	cfg := Config{
		Timezone: "UTC", ScheduleEnabled: true, OutsideSchedule: OutsideNormal,
		Windows: []Window{{ID: "day", Name: "All day", Days: []int{3}, Start: "09:00", End: "09:00", Action: ActionLimited, Limit: Limit{Mode: ModeKbps, Value: 512}}},
	}
	limit := int64(512 * 1024)
	wednesday := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{wednesday.Add(9 * time.Hour), wednesday.Add(23 * time.Hour), wednesday.Add(24*time.Hour - time.Second)} {
		if eff := Evaluate(cfg, at); eff.Paused || eff.LimitBytesPerSecond != limit {
			t.Errorf("Evaluate(%s) = %+v, want a full-day window", at.Format(time.RFC3339), eff)
		}
	}
	for _, at := range []time.Time{wednesday.Add(9*time.Hour - time.Second), wednesday.Add(24*time.Hour + 9*time.Hour)} {
		if eff := Evaluate(cfg, at); eff.LimitBytesPerSecond != 0 {
			t.Errorf("Evaluate(%s) = %+v, want outside the full-day window", at.Format(time.RFC3339), eff)
		}
	}
}

func TestEvaluateMidnightBoundary(t *testing.T) {
	cfg := Config{
		Timezone: "UTC", ScheduleEnabled: true, OutsideSchedule: OutsideNormal,
		Windows: []Window{{ID: "early", Days: []int{2, 3}, Start: "00:00", End: "06:00", Action: ActionLimited, Limit: Limit{Mode: ModeKbps, Value: 256}}},
	}
	limit := int64(256 * 1024)
	wednesday := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		at    time.Time
		bytes int64
	}{
		{wednesday.Add(-time.Second), 0}, // Tuesday 23:59:59
		{wednesday, limit},               // Wednesday 00:00
		{wednesday.Add(6*time.Hour - time.Second), limit},
		{wednesday.Add(6 * time.Hour), 0},  // end exclusive
		{wednesday.Add(24 * time.Hour), 0}, // Thursday is not selected
	}
	for _, tc := range cases {
		if eff := Evaluate(cfg, tc.at); eff.LimitBytesPerSecond != tc.bytes {
			t.Errorf("Evaluate(%s) = %+v, want %d bytes/s", tc.at.Format(time.RFC3339), eff, tc.bytes)
		}
	}
	next := Evaluate(cfg, wednesday.Add(-time.Hour)).NextChange
	if next == nil || !next.Equal(wednesday) {
		t.Fatalf("next change before midnight = %v, want the midnight boundary", next)
	}
}

func TestEvaluateDSTSpringForward(t *testing.T) {
	cfg := Config{
		Timezone: "America/New_York", ScheduleEnabled: true, OutsideSchedule: OutsideNormal,
		Windows: []Window{{ID: "early", Name: "Early", Days: []int{0, 1, 2, 3, 4, 5, 6}, Start: "02:00", End: "02:30", Action: ActionLimited, Limit: Limit{Mode: ModeKbps, Value: 100}}},
	}
	limit := int64(100 * 1024)
	// 2026-03-08 02:00 EST never occurs; the clock jumps from 01:59 EST (06:59 UTC) to 03:00 EDT (07:00 UTC).
	if eff := Evaluate(cfg, time.Date(2026, 3, 8, 6, 59, 0, 0, time.UTC)); eff.LimitBytesPerSecond != 0 {
		t.Fatalf("Evaluate before the jump = %+v, want outside", eff)
	}
	if eff := Evaluate(cfg, time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)); eff.LimitBytesPerSecond != 0 {
		t.Fatalf("Evaluate after the jump = %+v, want outside", eff)
	}
	next := Evaluate(cfg, time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC)).NextChange
	want := time.Date(2026, 3, 9, 6, 0, 0, 0, time.UTC) // 02:00 EDT
	if next == nil || !next.Equal(want) {
		t.Fatalf("next change across the spring-forward day = %v, want %v", next, want)
	}
	if eff := Evaluate(cfg, want); eff.LimitBytesPerSecond != limit {
		t.Fatalf("Evaluate at the next window = %+v, want %d", eff, limit)
	}
}

func TestEvaluateDSTFallBack(t *testing.T) {
	cfg := Config{
		Timezone: "America/New_York", ScheduleEnabled: true, OutsideSchedule: OutsideNormal,
		Windows: []Window{{ID: "early", Name: "Early", Days: []int{0, 1, 2, 3, 4, 5, 6}, Start: "01:00", End: "01:30", Action: ActionLimited, Limit: Limit{Mode: ModeKbps, Value: 100}}},
	}
	limit := int64(100 * 1024)
	// 2026-11-01: 01:00-01:30 EDT is 05:00-05:30 UTC and repeats as EST at 06:00-06:30 UTC.
	cases := []struct {
		at    time.Time
		bytes int64
	}{
		{time.Date(2026, 11, 1, 4, 59, 0, 0, time.UTC), 0},
		{time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC), limit},
		{time.Date(2026, 11, 1, 5, 29, 0, 0, time.UTC), limit},
		{time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), 0},
		{time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC), limit},
		{time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), 0},
	}
	for _, tc := range cases {
		if eff := Evaluate(cfg, tc.at); eff.LimitBytesPerSecond != tc.bytes {
			t.Errorf("Evaluate(%s) = %+v, want %d bytes/s", tc.at.Format(time.RFC3339), eff, tc.bytes)
		}
	}
	next := Evaluate(cfg, time.Date(2026, 11, 1, 5, 10, 0, 0, time.UTC)).NextChange
	if want := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC); next == nil || !next.Equal(want) {
		t.Fatalf("next change inside the repeated hour = %v, want %v", next, want)
	}
	next = Evaluate(cfg, time.Date(2026, 11, 1, 5, 40, 0, 0, time.UTC)).NextChange
	if want := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC); next == nil || !next.Equal(want) {
		t.Fatalf("next change after the repeated hour = %v, want %v", next, want)
	}
}

func TestEvaluatePercentAndKbpsLimits(t *testing.T) {
	fixed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	cfg := Config{Timezone: "UTC", ConnectionMbps: 100, Limit: Limit{Mode: ModePercent, Value: 50}, OutsideSchedule: OutsideNormal}
	eff := Evaluate(cfg, fixed)
	if eff.LimitBytesPerSecond != 6_250_000 || eff.Reason != "schedule off allows 50% of 100 Mbps" {
		t.Fatalf("percent base limit = %+v", eff)
	}
	cfg.ScheduleEnabled = true
	cfg.Windows = []Window{{ID: "day", Days: []int{1}, Start: "00:00", End: "00:00", Action: ActionLimited, Limit: Limit{Mode: ModePercent, Value: 10}}}
	eff = Evaluate(cfg, fixed)
	if eff.LimitBytesPerSecond != 1_250_000 || eff.Reason != `window "day" limits to 10% of 100 Mbps` {
		t.Fatalf("percent window limit = %+v", eff)
	}
	cfg.Windows[0].Limit = Limit{Mode: ModeKbps, Value: 512.5}
	if eff = Evaluate(cfg, fixed); eff.LimitBytesPerSecond != 524_800 {
		t.Fatalf("kbps window limit = %+v", eff)
	}
}

func TestEvaluateNoScheduledChange(t *testing.T) {
	cfg := Config{
		Timezone: "UTC", ScheduleEnabled: true, OutsideSchedule: OutsideNormal,
		Windows: []Window{
			{ID: "a", Days: []int{0}, Start: "00:00", End: "12:00", Action: ActionFull},
			{ID: "b", Days: []int{0}, Start: "12:00", End: "00:00", Action: ActionFull},
		},
	}
	if eff := Evaluate(cfg, time.Date(2026, 10, 4, 6, 0, 0, 0, time.UTC)); eff.NextChange != nil || eff.Paused || eff.LimitBytesPerSecond != 0 {
		t.Fatalf("full-speed schedule = %+v, want no change", eff)
	}
}

func TestValidateAcceptsAndNormalizes(t *testing.T) {
	cfg := Config{
		Limit: Limit{Mode: ""}, OutsideSchedule: "", ConnectionMbps: 0,
		Windows: []Window{{ID: " w1 ", Days: []int{6}, Start: "23:00", End: "02:00", Action: ActionPaused}},
	}
	got, err := validate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.Timezone != "UTC" || got.Limit.Mode != ModeUnlimited || got.OutsideSchedule != OutsideNormal {
		t.Fatalf("normalized config = %+v", got)
	}
	if got.Windows[0].ID != "w1" || got.Windows[0].Limit.Mode != ModeUnlimited {
		t.Fatalf("normalized window = %+v", got.Windows[0])
	}
	windows := make([]Window, 32)
	for i := range windows {
		windows[i] = Window{ID: string(rune('a' + i)), Days: []int{0}, Start: clockLabel(i * 30), End: clockLabel(i*30 + 30), Action: ActionPaused}
	}
	full := Config{Timezone: "UTC", ScheduleEnabled: false, Windows: windows}
	if _, err := validate(full); err != nil {
		t.Fatalf("disabled schedule with valid windows: %v", err)
	}
	full.Windows = append(full.Windows, Window{ID: "extra", Days: []int{0}, Start: "02:00", End: "02:30", Action: ActionPaused})
	if _, err := validate(full); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("33 windows error = %v, want ErrInvalid", err)
	}
}

func TestValidateRejects(t *testing.T) {
	base := func() Config {
		return Config{
			Timezone: "UTC", ConnectionMbps: 100, Limit: Limit{Mode: ModeKbps, Value: 1024},
			ScheduleEnabled: true, OutsideSchedule: OutsideNormal,
			Windows: []Window{{ID: "w1", Name: "Window", Days: []int{1}, Start: "06:00", End: "08:00", Action: ActionLimited, Limit: Limit{Mode: ModeKbps, Value: 256}}},
		}
	}
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"timezone", func(c *Config) { c.Timezone = "Mars/Olympus" }, "timezone"},
		{"connection negative", func(c *Config) { c.ConnectionMbps = -1 }, "connection speed"},
		{"connection infinite", func(c *Config) { c.ConnectionMbps = 1e12 }, "connection speed"},
		{"outside schedule", func(c *Config) { c.OutsideSchedule = "sometimes" }, "outside schedule"},
		{"base mode", func(c *Config) { c.Limit = Limit{Mode: "fastest", Value: 1} }, "limit mode"},
		{"base kbps zero", func(c *Config) { c.Limit = Limit{Mode: ModeKbps} }, "kbps limit"},
		{"base kbps negative", func(c *Config) { c.Limit = Limit{Mode: ModeKbps, Value: -5} }, "kbps limit"},
		{"base kbps over range", func(c *Config) { c.Limit = Limit{Mode: ModeKbps, Value: 1e12} }, "kbps limit"},
		{"base percent zero", func(c *Config) { c.Limit = Limit{Mode: ModePercent} }, "percent limit"},
		{"base percent over", func(c *Config) { c.Limit = Limit{Mode: ModePercent, Value: 100.5} }, "percent limit"},
		{"base percent without mbps", func(c *Config) { c.ConnectionMbps = 0; c.Limit = Limit{Mode: ModePercent, Value: 50} }, "connection speed in Mbps"},
		{"window id empty", func(c *Config) { c.Windows[0].ID = " " }, "id must be"},
		{"window id long", func(c *Config) { c.Windows[0].ID = strings.Repeat("i", 65) }, "id must be"},
		{"window name long", func(c *Config) { c.Windows[0].Name = strings.Repeat("n", 121) }, "name must be"},
		{"window days empty", func(c *Config) { c.Windows[0].Days = nil }, "weekday is required"},
		{"window day range", func(c *Config) { c.Windows[0].Days = []int{7} }, "weekday 7"},
		{"window day duplicate", func(c *Config) { c.Windows[0].Days = []int{1, 1} }, "duplicated"},
		{"window start format", func(c *Config) { c.Windows[0].Start = "6:00" }, "HH:MM"},
		{"window start hour", func(c *Config) { c.Windows[0].Start = "24:00" }, "between 00:00"},
		{"window end minute", func(c *Config) { c.Windows[0].End = "08:60" }, "between 00:00"},
		{"window action", func(c *Config) { c.Windows[0].Action = "turbo" }, "action"},
		{"window limited without cap", func(c *Config) { c.Windows[0].Limit = Limit{Mode: ModeUnlimited} }, "kbps or percent"},
		{"window limited without mbps", func(c *Config) { c.ConnectionMbps = 0; c.Windows[0].Limit = Limit{Mode: ModePercent, Value: 50} }, "connection speed in Mbps"},
		{"window id duplicate", func(c *Config) { c.Windows = append(c.Windows, c.Windows[0]) }, "not unique"},
		{"same-day overlap", func(c *Config) {
			c.Windows = append(c.Windows, Window{ID: "late", Days: []int{1}, Start: "07:00", End: "09:00", Action: ActionPaused})
		}, "overlap"},
		{"cross-midnight overlap", func(c *Config) {
			c.Windows = []Window{
				{ID: "night", Days: []int{0}, Start: "23:00", End: "02:00", Action: ActionPaused},
				{ID: "late", Days: []int{1}, Start: "00:30", End: "01:30", Action: ActionPaused},
			}
		}, "overlap"},
		{"overlap while disabled", func(c *Config) {
			c.ScheduleEnabled = false
			c.Windows = []Window{
				{ID: "a", Days: []int{2}, Start: "10:00", End: "11:00", Action: ActionPaused},
				{ID: "b", Days: []int{2}, Start: "10:30", End: "11:30", Action: ActionPaused},
			}
		}, "overlap"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(&cfg)
			_, err := validate(cfg)
			if err == nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("validate error = %v, want ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
	if _, err := validate(base()); err != nil {
		t.Fatalf("valid config: %v", err)
	}
}

func TestEvaluateManualPauseOverridesSchedule(t *testing.T) {
	cfg := Config{
		Paused: true, Timezone: "UTC", ScheduleEnabled: true, OutsideSchedule: OutsideNormal,
		Windows: []Window{{ID: "w", Days: []int{1}, Start: "00:00", End: "00:00", Action: ActionLimited, Limit: Limit{Mode: ModeKbps, Value: 128}}},
	}
	eff := Evaluate(cfg, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	if !eff.Paused || eff.LimitBytesPerSecond != 0 || eff.Reason != "paused manually" || eff.NextChange != nil {
		t.Fatalf("manual pause = %+v", eff)
	}
}

func TestEvaluateOutsideSchedulePaused(t *testing.T) {
	cfg := Config{
		Timezone: "UTC", ScheduleEnabled: true, OutsideSchedule: OutsidePaused,
		Windows: []Window{{ID: "w", Days: []int{1}, Start: "06:00", End: "08:00", Action: ActionFull}},
	}
	if eff := Evaluate(cfg, time.Date(2026, 10, 5, 5, 0, 0, 0, time.UTC)); !eff.Paused || eff.Reason != "outside schedule is paused" {
		t.Fatalf("outside schedule = %+v", eff)
	}
	if eff := Evaluate(cfg, time.Date(2026, 10, 5, 7, 0, 0, 0, time.UTC)); eff.Paused || eff.Reason != `window "w" allows full speed` {
		t.Fatalf("inside full window = %+v", eff)
	}
}

type fakeClock struct{ nanos atomic.Int64 }

func newFakeClock(at time.Time) *fakeClock {
	clock := &fakeClock{}
	clock.set(at)
	return clock
}

func (c *fakeClock) set(at time.Time) { c.nanos.Store(at.UnixNano()) }
func (c *fakeClock) now() time.Time   { return time.Unix(0, c.nanos.Load()).UTC() }

func testController(t *testing.T, at time.Time, cfg Config) (*Controller, *fakeClock) {
	t.Helper()
	validated, err := validate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock(at)
	c := &Controller{
		limiter: rate.NewLimiter(rate.Inf, burstBytes), cfg: validated, now: clock.now,
	}
	c.refresh(clock.now())
	return c, clock
}

func TestControllerFollowsClockAndCachesNextChange(t *testing.T) {
	cfg := Config{
		Timezone: "UTC", Limit: Limit{Mode: ModeKbps, Value: 1024}, ConnectionMbps: 10,
		ScheduleEnabled: true, OutsideSchedule: OutsideNormal,
		Windows: []Window{
			{ID: "morning", Name: "Morning", Days: []int{1}, Start: "06:00", End: "08:00", Action: ActionLimited, Limit: Limit{Mode: ModeKbps, Value: 256}},
			{ID: "sleep", Name: "Sleep", Days: []int{1}, Start: "12:00", End: "13:00", Action: ActionPaused},
		},
	}
	monday := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	c, clock := testController(t, monday.Add(5*time.Hour), cfg)
	limiter := c.Limiter()
	if limiter.Limit() != rate.Limit(1024*1024) || !c.Allowed() {
		t.Fatalf("initial limiter = %v, allowed = %v", limiter.Limit(), c.Allowed())
	}
	if next := c.Snapshot().Effective.NextChange; next == nil || !next.Equal(monday.Add(6*time.Hour)) {
		t.Fatalf("initial next change = %v, want 06:00", next)
	}
	clock.set(monday.Add(5*time.Hour + 30*time.Minute))
	if next := c.Snapshot().Effective.NextChange; next == nil || !next.Equal(monday.Add(6*time.Hour)) {
		t.Fatalf("cached next change = %v, want 06:00", next)
	}
	clock.set(monday.Add(6*time.Hour + 30*time.Minute))
	c.refresh(clock.now())
	if limiter != c.Limiter() || limiter.Limit() != rate.Limit(256*1024) || !c.Allowed() {
		t.Fatalf("window limiter = %v, allowed = %v", limiter.Limit(), c.Allowed())
	}
	if next := c.Snapshot().Effective.NextChange; next == nil || !next.Equal(monday.Add(8*time.Hour)) {
		t.Fatalf("next change after the window = %v, want 08:00", next)
	}
	clock.set(monday.Add(12*time.Hour + 30*time.Minute))
	c.refresh(clock.now())
	if limiter.Limit() != 0 || c.Allowed() {
		t.Fatalf("paused limiter = %v, allowed = %v", limiter.Limit(), c.Allowed())
	}
	clock.set(monday.Add(13 * time.Hour))
	c.refresh(clock.now())
	if limiter.Limit() != rate.Limit(1024*1024) || !c.Allowed() {
		t.Fatalf("resumed limiter = %v, allowed = %v", limiter.Limit(), c.Allowed())
	}
}

func TestControllerRunAppliesChangesUntilCancelled(t *testing.T) {
	cfg := Config{
		Timezone: "UTC", ScheduleEnabled: true, OutsideSchedule: OutsideNormal,
		Windows: []Window{{ID: "sleep", Name: "Sleep", Days: []int{1}, Start: "06:00", End: "08:00", Action: ActionPaused}},
	}
	monday := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	c, clock := testController(t, monday.Add(5*time.Hour), cfg)
	if !c.Allowed() || c.Limiter().Limit() != rate.Inf {
		t.Fatalf("before the window: allowed = %v, limiter = %v", c.Allowed(), c.Limiter().Limit())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Run(ctx)
		close(done)
	}()
	clock.set(monday.Add(6*time.Hour + 30*time.Minute))
	deadline := time.Now().Add(3 * time.Second)
	for c.Limiter().Limit() != 0 || c.Allowed() {
		if time.Now().After(deadline) {
			t.Fatalf("Run did not apply the pause: limiter = %v, allowed = %v", c.Limiter().Limit(), c.Allowed())
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("Run returned before the context was cancelled")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

func TestSnapshotCopiesSlicesAndPointers(t *testing.T) {
	cfg := Config{
		Timezone: "UTC", ScheduleEnabled: true, OutsideSchedule: OutsideNormal,
		Windows: []Window{{ID: "w", Days: []int{1}, Start: "06:00", End: "08:00", Action: ActionLimited, Limit: Limit{Mode: ModeKbps, Value: 256}}},
	}
	monday := time.Date(2026, 10, 5, 5, 0, 0, 0, time.UTC)
	c, _ := testController(t, monday, cfg)
	snapshot := c.Snapshot()
	if snapshot.Effective.NextChange == nil {
		t.Fatal("expected a next change")
	}
	snapshot.Config.Windows[0].Days[0] = 6
	snapshot.Config.Windows[0].Name = "changed"
	snapshot.Config.Windows = append(snapshot.Config.Windows, Window{ID: "extra"})
	*snapshot.Effective.NextChange = monday
	again := c.Snapshot()
	if again.Config.Windows[0].Days[0] != 1 || again.Config.Windows[0].Name != "" || len(again.Config.Windows) != 1 {
		t.Fatalf("snapshot shares config state: %+v", again.Config)
	}
	if !again.Effective.NextChange.Equal(monday.Add(time.Hour)) {
		t.Fatalf("snapshot shares the next change pointer: %v", again.Effective.NextChange)
	}
}

func clockLabel(minutes int) string {
	return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute).Format("15:04")
}
