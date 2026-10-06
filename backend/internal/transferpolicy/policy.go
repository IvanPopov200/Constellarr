// Package transferpolicy resolves the persisted global download policy into one shared rate limiter.
package transferpolicy

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalid = errors.New("transferpolicy: invalid input")

const (
	ModeUnlimited = "unlimited"
	ModeKbps      = "kbps"
	ModePercent   = "percent"

	ActionFull    = "full"
	ActionLimited = "limited"
	ActionPaused  = "paused"

	OutsideNormal = "normal"
	OutsidePaused = "paused"
)

const (
	maxWindows   = 32
	maxIDBytes   = 64
	maxNameRunes = 120
	maxKbps      = 1 << 30
	maxMbps      = 1_000_000
	weekMinutes  = 7 * 24 * 60
	scanDays     = 8
)

// Limit caps transfer speed; kbps is KiB/s and percent is of the entered connection speed.
type Limit struct {
	Mode  string  `json:"mode"`
	Value float64 `json:"value"`
}

type Window struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Days   []int  `json:"days"`
	Start  string `json:"start"`
	End    string `json:"end"`
	Action string `json:"action"`
	Limit  Limit  `json:"limit"`
}

type Config struct {
	Paused          bool     `json:"paused"`
	Timezone        string   `json:"timezone"`
	ConnectionMbps  float64  `json:"connectionMbps"`
	Limit           Limit    `json:"limit"`
	ScheduleEnabled bool     `json:"scheduleEnabled"`
	OutsideSchedule string   `json:"outsideSchedule"`
	Windows         []Window `json:"windows"`
}

// Effective is the resolved policy at an instant; a zero LimitBytesPerSecond means unlimited and a paused policy holds transfers.
type Effective struct {
	Paused              bool       `json:"paused"`
	Reason              string     `json:"reason"`
	LimitBytesPerSecond int64      `json:"limitBytesPerSecond"`
	NextChange          *time.Time `json:"nextChange,omitempty"`
}

type Snapshot struct {
	Config    Config    `json:"config"`
	Effective Effective `json:"effective"`
}

func Default() Config {
	return Config{
		Timezone: "UTC", Limit: Limit{Mode: ModeUnlimited},
		OutsideSchedule: OutsideNormal, Windows: []Window{},
	}
}

type invalidError string

func (e invalidError) Error() string        { return string(e) }
func (e invalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(format string, args ...any) error { return invalidError(fmt.Sprintf(format, args...)) }

func validate(cfg Config) (Config, error) {
	cfg = normalize(cfg)
	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		return Config{}, invalid("timezone %q is not a known IANA location", cfg.Timezone)
	}
	if !finite(cfg.ConnectionMbps) || cfg.ConnectionMbps < 0 || cfg.ConnectionMbps > maxMbps {
		return Config{}, invalid("connection speed must be a finite value between 0 and %d Mbps", maxMbps)
	}
	switch cfg.OutsideSchedule {
	case OutsideNormal, OutsidePaused:
	default:
		return Config{}, invalid("outside schedule mode %q must be %s or %s", cfg.OutsideSchedule, OutsideNormal, OutsidePaused)
	}
	if err := validateLimit(cfg.Limit, cfg.ConnectionMbps); err != nil {
		return Config{}, err
	}
	if len(cfg.Windows) > maxWindows {
		return Config{}, invalid("at most %d schedule windows are supported", maxWindows)
	}
	ids := make(map[string]bool, len(cfg.Windows))
	for i, window := range cfg.Windows {
		if err := validateWindow(window, cfg.ConnectionMbps); err != nil {
			return Config{}, fmt.Errorf("window %d: %w", i+1, err)
		}
		if ids[window.ID] {
			return Config{}, invalid("window id %q is not unique", window.ID)
		}
		ids[window.ID] = true
	}
	if err := checkOverlaps(cfg.Windows); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateLimit(limit Limit, mbps float64) error {
	switch limit.Mode {
	case ModeUnlimited:
		return nil
	case ModeKbps:
		if !finite(limit.Value) || limit.Value <= 0 || limit.Value > maxKbps {
			return invalid("kbps limit must be a finite value between 0 and %d KiB/s", maxKbps)
		}
	case ModePercent:
		if !finite(limit.Value) || limit.Value <= 0 || limit.Value > 100 {
			return invalid("percent limit must be greater than 0 and at most 100")
		}
		if !finite(mbps) || mbps <= 0 {
			return invalid("a connection speed in Mbps is required for percent limits")
		}
	default:
		return invalid("limit mode %q must be %s, %s, or %s", limit.Mode, ModeUnlimited, ModeKbps, ModePercent)
	}
	return nil
}

func validateWindow(window Window, mbps float64) error {
	if window.ID == "" || len(window.ID) > maxIDBytes {
		return invalid("id must be between 1 and %d bytes", maxIDBytes)
	}
	if utf8.RuneCountInString(window.Name) > maxNameRunes {
		return invalid("name must be at most %d characters", maxNameRunes)
	}
	if len(window.Days) == 0 {
		return invalid("at least one weekday is required")
	}
	if len(window.Days) > 7 {
		return invalid("at most seven weekdays are allowed")
	}
	seen := make(map[int]bool, len(window.Days))
	for _, day := range window.Days {
		if day < 0 || day > 6 {
			return invalid("weekday %d must be between 0 (Sunday) and 6 (Saturday)", day)
		}
		if seen[day] {
			return invalid("weekday %d is duplicated", day)
		}
		seen[day] = true
	}
	if _, err := parseClock(window.Start); err != nil {
		return err
	}
	if _, err := parseClock(window.End); err != nil {
		return err
	}
	switch window.Action {
	case ActionFull, ActionPaused:
		return nil // these actions ignore the window limit
	case ActionLimited:
		if window.Limit.Mode == ModeUnlimited {
			return invalid("limited windows require a kbps or percent limit")
		}
		return validateLimit(window.Limit, mbps)
	default:
		return invalid("action %q must be %s, %s, or %s", window.Action, ActionFull, ActionLimited, ActionPaused)
	}
}

func normalize(cfg Config) Config {
	cfg.Timezone = strings.TrimSpace(cfg.Timezone)
	if cfg.Timezone == "" {
		cfg.Timezone = "UTC"
	}
	cfg.Limit.Mode = normalizeMode(cfg.Limit.Mode)
	if cfg.OutsideSchedule == "" {
		cfg.OutsideSchedule = OutsideNormal
	}
	if cfg.Windows == nil {
		cfg.Windows = []Window{}
	}
	for i := range cfg.Windows {
		window := &cfg.Windows[i]
		window.ID = strings.TrimSpace(window.ID)
		window.Name = strings.TrimSpace(window.Name)
		window.Action = strings.TrimSpace(window.Action)
		window.Start = strings.TrimSpace(window.Start)
		window.End = strings.TrimSpace(window.End)
		window.Limit.Mode = normalizeMode(window.Limit.Mode)
		if window.Days == nil {
			window.Days = []int{}
		}
	}
	return cfg
}

func normalizeMode(mode string) string {
	if mode = strings.TrimSpace(mode); mode == "" {
		return ModeUnlimited
	}
	return mode
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func parseClock(value string) (int, error) {
	if len(value) != 5 || value[2] != ':' || !digits(value[:2]) || !digits(value[3:]) {
		return 0, invalid("time %q must be HH:MM", value)
	}
	hour := int(value[0]-'0')*10 + int(value[1]-'0')
	minute := int(value[3]-'0')*10 + int(value[4]-'0')
	if hour > 23 || minute > 59 {
		return 0, invalid("time %q must be between 00:00 and 23:59", value)
	}
	return hour*60 + minute, nil
}

func digits(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

// checkOverlaps rejects windows sharing a week minute so no hidden priority order exists.
func checkOverlaps(windows []Window) error {
	occupied := make([]int8, weekMinutes)
	for i := range windows {
		var clash error
		forEachMinute(windows[i], func(minute int) {
			if other := occupied[minute]; other != 0 && clash == nil {
				clash = invalid("windows %q and %q overlap", windowLabel(windows[other-1]), windowLabel(windows[i]))
			}
			occupied[minute] = int8(i + 1)
		})
		if clash != nil {
			return clash
		}
	}
	return nil
}

// forEachMinute visits every week minute covered by a window, wrapping past Saturday midnight.
func forEachMinute(window Window, visit func(int)) {
	start, span := windowRange(window)
	for _, day := range window.Days {
		if day < 0 || day > 6 {
			continue
		}
		base := day*24*60 + start
		for offset := range span {
			visit(((base+offset)%weekMinutes + weekMinutes) % weekMinutes)
		}
	}
}

// windowRange returns the start minute and length; equal start and end span a full day.
func windowRange(window Window) (int, int) {
	start, _ := parseClock(window.Start)
	end, _ := parseClock(window.End)
	span := end - start
	if span <= 0 {
		span += 24 * 60
	}
	return start, span
}

func windowActive(window Window, weekMinute int) bool {
	start, span := windowRange(window)
	for _, day := range window.Days {
		if day < 0 || day > 6 {
			continue
		}
		delta := ((weekMinute-day*24*60-start)%weekMinutes + weekMinutes) % weekMinutes
		if delta < span {
			return true
		}
	}
	return false
}

// scheduleIndex maps week minutes to window indexes plus one so long scans stay linear.
func scheduleIndex(windows []Window) []int8 {
	index := make([]int8, weekMinutes)
	for i := range windows {
		forEachMinute(windows[i], func(minute int) { index[minute] = int8(i + 1) })
	}
	return index
}

func Evaluate(cfg Config, now time.Time) Effective {
	eff := evaluate(cfg, now, nil)
	if next, ok := nextChangeAt(cfg, now); ok {
		eff.NextChange = &next
	}
	return eff
}

func evaluate(cfg Config, now time.Time, index []int8) Effective {
	return decide(cfg, now, index).effective(cfg)
}

type verdict struct {
	paused  bool
	bytes   int64
	manual  bool
	off     bool
	outside bool
	window  *Window
}

func decide(cfg Config, now time.Time, index []int8) verdict {
	return decideAt(cfg, now, index, location(cfg.Timezone))
}

func decideAt(cfg Config, now time.Time, index []int8, loc *time.Location) verdict {
	if cfg.Paused {
		return verdict{paused: true, manual: true}
	}
	if !cfg.ScheduleEnabled {
		return verdict{bytes: limitBytes(cfg.Limit, cfg.ConnectionMbps), off: true}
	}
	window := activeWindow(cfg.Windows, now.In(loc), index)
	if window == nil {
		if cfg.OutsideSchedule == OutsidePaused {
			return verdict{paused: true, outside: true}
		}
		return verdict{bytes: limitBytes(cfg.Limit, cfg.ConnectionMbps), outside: true}
	}
	switch window.Action {
	case ActionPaused:
		return verdict{paused: true, window: window}
	case ActionLimited:
		return verdict{bytes: limitBytes(window.Limit, cfg.ConnectionMbps), window: window}
	default:
		return verdict{window: window}
	}
}

func (v verdict) effective(cfg Config) Effective {
	eff := Effective{Paused: v.paused, LimitBytesPerSecond: v.bytes}
	switch {
	case v.manual:
		eff.Reason = "paused manually"
	case v.window != nil:
		name := windowLabel(*v.window)
		switch {
		case v.paused:
			eff.Reason = fmt.Sprintf("window %q is paused", name)
		case v.window.Action == ActionLimited:
			eff.Reason = fmt.Sprintf("window %q limits to %s", name, limitLabel(v.window.Limit, cfg.ConnectionMbps))
		default:
			eff.Reason = fmt.Sprintf("window %q allows full speed", name)
		}
	case v.outside:
		if v.paused {
			eff.Reason = "outside schedule is paused"
		} else {
			eff.Reason = "outside schedule allows " + limitLabel(cfg.Limit, cfg.ConnectionMbps)
		}
	default:
		eff.Reason = "schedule off allows " + limitLabel(cfg.Limit, cfg.ConnectionMbps)
	}
	return eff
}

func activeWindow(windows []Window, local time.Time, index []int8) *Window {
	weekMinute := (int(local.Weekday())*24+local.Hour())*60 + local.Minute()
	if index != nil {
		if i := index[weekMinute]; i > 0 {
			return &windows[i-1]
		}
		return nil
	}
	for i := range windows {
		if windowActive(windows[i], weekMinute) {
			return &windows[i]
		}
	}
	return nil
}

// nextChangeAt scans minute boundaries for up to eight days, which keeps overnight windows and DST exact.
func nextChangeAt(cfg Config, now time.Time) (time.Time, bool) {
	if cfg.Paused || !cfg.ScheduleEnabled || len(cfg.Windows) == 0 {
		return time.Time{}, false
	}
	index := scheduleIndex(cfg.Windows)
	loc := location(cfg.Timezone)
	current := decideAt(cfg, now, index, loc)
	start := now.Truncate(time.Minute)
	end := start.Add(scanDays * 24 * time.Hour)
	for at := start.Add(time.Minute); !at.After(end); at = at.Add(time.Minute) {
		next := decideAt(cfg, at, index, loc)
		if next.paused != current.paused || next.bytes != current.bytes {
			return at.In(loc), true
		}
	}
	return time.Time{}, false
}

func limitBytes(limit Limit, mbps float64) int64 {
	switch limit.Mode {
	case ModeKbps:
		if limit.Value > 0 {
			return max(1, int64(math.Round(limit.Value*1024)))
		}
	case ModePercent:
		if limit.Value > 0 && mbps > 0 {
			return max(1, int64(math.Round(mbps*1e6/8*limit.Value/100)))
		}
	}
	return 0
}

func limitLabel(limit Limit, mbps float64) string {
	switch limit.Mode {
	case ModeKbps:
		return formatFloat(limit.Value) + " KiB/s"
	case ModePercent:
		return formatFloat(limit.Value) + "% of " + formatFloat(mbps) + " Mbps"
	default:
		return "unlimited"
	}
}

func windowLabel(window Window) string {
	if window.Name != "" {
		return window.Name
	}
	return window.ID
}

func formatFloat(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }

func location(name string) *time.Location {
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}
