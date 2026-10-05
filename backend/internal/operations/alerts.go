package operations

import (
	"encoding/json"
	"fmt"
	"math"
)

var ruleNames = []string{"filesystem_low_space", "provider_failures", "download_import_failures", "job_stuck", "db_health"}

func validRuleName(name string) bool {
	for _, known := range ruleNames {
		if known == name {
			return true
		}
	}
	return false
}

func normalizeSeverity(severity string) string {
	switch severity {
	case severityInfo, severityWarning, severityCritical:
		return severity
	}
	return severityWarning
}

func defaultSeverity(name string) string {
	if name == "db_health" {
		return severityCritical
	}
	return severityWarning
}

// Thresholds are stored per rule; unused fields stay absent so the API can present effective defaults.
type alertThresholds struct {
	MinimumFreeBytes   *int64   `json:"minimumFreeBytes,omitempty"`
	MinimumFreePercent *float64 `json:"minimumFreePercent,omitempty"`
	WindowMinutes      *int     `json:"windowMinutes,omitempty"`
	Threshold          *int     `json:"threshold,omitempty"`
	StuckMinutes       *int     `json:"stuckMinutes,omitempty"`
	Failures           *int     `json:"failures,omitempty"`
}

func intPointer(value int) *int           { return &value }
func int64Pointer(value int64) *int64     { return &value }
func floatPointer(value float64) *float64 { return &value }

func defaultThresholds(name string) alertThresholds {
	switch name {
	case "filesystem_low_space":
		return alertThresholds{MinimumFreeBytes: int64Pointer(10 << 30), MinimumFreePercent: floatPointer(10)}
	case "provider_failures":
		return alertThresholds{WindowMinutes: intPointer(60), Threshold: intPointer(3)}
	case "download_import_failures":
		return alertThresholds{WindowMinutes: intPointer(60), Threshold: intPointer(3)}
	case "job_stuck":
		return alertThresholds{StuckMinutes: intPointer(120)}
	case "db_health":
		return alertThresholds{Failures: intPointer(2)}
	}
	return alertThresholds{}
}

type alertRule struct {
	name       string
	enabled    bool
	severity   string
	thresholds alertThresholds
}

func mergeThresholds(defaults alertThresholds, raw []byte) alertThresholds {
	stored := alertThresholds{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &stored)
	}
	merged := defaults
	if stored.MinimumFreeBytes != nil {
		merged.MinimumFreeBytes = stored.MinimumFreeBytes
	}
	if stored.MinimumFreePercent != nil {
		merged.MinimumFreePercent = stored.MinimumFreePercent
	}
	if stored.WindowMinutes != nil {
		merged.WindowMinutes = stored.WindowMinutes
	}
	if stored.Threshold != nil {
		merged.Threshold = stored.Threshold
	}
	if stored.StuckMinutes != nil {
		merged.StuckMinutes = stored.StuckMinutes
	}
	if stored.Failures != nil {
		merged.Failures = stored.Failures
	}
	return merged
}

type ThresholdView struct {
	MinimumFreeBytes   int64   `json:"minimumFreeBytes"`
	MinimumFreePercent float64 `json:"minimumFreePercent"`
	WindowMinutes      int     `json:"windowMinutes"`
	Threshold          int     `json:"threshold"`
	StuckMinutes       int     `json:"stuckMinutes"`
	Failures           int     `json:"failures"`
}

func viewOf(thresholds alertThresholds) ThresholdView {
	view := ThresholdView{}
	if thresholds.MinimumFreeBytes != nil {
		view.MinimumFreeBytes = *thresholds.MinimumFreeBytes
	}
	if thresholds.MinimumFreePercent != nil {
		view.MinimumFreePercent = *thresholds.MinimumFreePercent
	}
	if thresholds.WindowMinutes != nil {
		view.WindowMinutes = *thresholds.WindowMinutes
	}
	if thresholds.Threshold != nil {
		view.Threshold = *thresholds.Threshold
	}
	if thresholds.StuckMinutes != nil {
		view.StuckMinutes = *thresholds.StuckMinutes
	}
	if thresholds.Failures != nil {
		view.Failures = *thresholds.Failures
	}
	return view
}

type alertInputs struct {
	freeBytes        int64
	totalBytes       int64
	storageKnown     bool
	dbFailures       int
	failuresInWindow int
	providerInWindow int
	stuckJobs        int
}

type evaluation struct {
	firing  bool
	value   float64
	message string
}

func evaluateRule(rule alertRule, inputs alertInputs) evaluation {
	switch rule.name {
	case "filesystem_low_space":
		if !inputs.storageKnown {
			return evaluation{}
		}
		percent := 0.0
		if inputs.totalBytes > 0 {
			percent = float64(inputs.freeBytes) / float64(inputs.totalBytes) * 100
		}
		minimumBytes := int64(0)
		if rule.thresholds.MinimumFreeBytes != nil {
			minimumBytes = *rule.thresholds.MinimumFreeBytes
		}
		minimumPercent := 0.0
		if rule.thresholds.MinimumFreePercent != nil {
			minimumPercent = *rule.thresholds.MinimumFreePercent
		}
		byBytes := minimumBytes > 0 && inputs.freeBytes < minimumBytes
		byPercent := minimumPercent > 0 && inputs.totalBytes > 0 && percent < minimumPercent
		if !byBytes && !byPercent {
			return evaluation{value: float64(inputs.freeBytes)}
		}
		return evaluation{
			firing:  true,
			value:   float64(inputs.freeBytes),
			message: "Free space on the data volume is below the configured threshold.",
		}
	case "provider_failures":
		window, threshold := windowThreshold(rule.thresholds, 60, 3)
		if inputs.providerInWindow > threshold {
			return evaluation{
				firing:  true,
				value:   float64(inputs.providerInWindow),
				message: fmt.Sprintf("%d provider failures in the last %d minutes.", inputs.providerInWindow, window),
			}
		}
		return evaluation{value: float64(inputs.providerInWindow)}
	case "download_import_failures":
		window, threshold := windowThreshold(rule.thresholds, 60, 3)
		total := inputs.failuresInWindow
		if total > threshold {
			return evaluation{
				firing:  true,
				value:   float64(total),
				message: fmt.Sprintf("%d download or import failures in the last %d minutes.", total, window),
			}
		}
		return evaluation{value: float64(total)}
	case "job_stuck":
		stuckMinutes := 120
		if rule.thresholds.StuckMinutes != nil {
			stuckMinutes = *rule.thresholds.StuckMinutes
		}
		if inputs.stuckJobs > 0 {
			return evaluation{
				firing:  true,
				value:   float64(inputs.stuckJobs),
				message: fmt.Sprintf("%d jobs have not progressed for more than %d minutes.", inputs.stuckJobs, stuckMinutes),
			}
		}
		return evaluation{}
	case "db_health":
		limit := 2
		if rule.thresholds.Failures != nil {
			limit = *rule.thresholds.Failures
		}
		if inputs.dbFailures >= limit {
			return evaluation{
				firing:  true,
				value:   float64(inputs.dbFailures),
				message: fmt.Sprintf("The database has been unreachable for %d consecutive checks.", inputs.dbFailures),
			}
		}
		return evaluation{value: float64(inputs.dbFailures)}
	}
	return evaluation{}
}

func windowThreshold(thresholds alertThresholds, defaultWindow, defaultThreshold int) (int, int) {
	window, threshold := defaultWindow, defaultThreshold
	if thresholds.WindowMinutes != nil {
		window = *thresholds.WindowMinutes
	}
	if thresholds.Threshold != nil {
		threshold = *thresholds.Threshold
	}
	return window, threshold
}

func thresholdsFromView(name string, view ThresholdView) (alertThresholds, error) {
	thresholds := alertThresholds{}
	invalid := func(message string) error { return InvalidError(message) }
	switch name {
	case "filesystem_low_space":
		if view.MinimumFreeBytes < 0 || view.MinimumFreeBytes > 1<<50 {
			return thresholds, invalid("minimum free bytes must be between 0 and 1 PiB")
		}
		if view.MinimumFreePercent < 0 || view.MinimumFreePercent > 100 {
			return thresholds, invalid("minimum free percentage must be between 0 and 100")
		}
		if view.MinimumFreeBytes == 0 && view.MinimumFreePercent == 0 {
			return thresholds, invalid("set a minimum free space in bytes or percent")
		}
		thresholds.MinimumFreeBytes = int64Pointer(view.MinimumFreeBytes)
		thresholds.MinimumFreePercent = floatPointer(view.MinimumFreePercent)
	case "provider_failures", "download_import_failures":
		if view.WindowMinutes < 1 || view.WindowMinutes > 1440 {
			return thresholds, invalid("window minutes must be between 1 and 1440")
		}
		if view.Threshold < 0 || view.Threshold > 1000 {
			return thresholds, invalid("threshold must be between 0 and 1000")
		}
		thresholds.WindowMinutes = intPointer(view.WindowMinutes)
		thresholds.Threshold = intPointer(view.Threshold)
	case "job_stuck":
		if view.StuckMinutes < 1 || view.StuckMinutes > 10080 {
			return thresholds, invalid("stuck minutes must be between 1 and 10080")
		}
		thresholds.StuckMinutes = intPointer(view.StuckMinutes)
	case "db_health":
		if view.Failures < 1 || view.Failures > 60 {
			return thresholds, invalid("failures must be between 1 and 60")
		}
		thresholds.Failures = intPointer(view.Failures)
	default:
		return thresholds, invalid("unknown alert rule")
	}
	return thresholds, nil
}

func storagePercent(free, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(free)/float64(total)*1000) / 10
}
