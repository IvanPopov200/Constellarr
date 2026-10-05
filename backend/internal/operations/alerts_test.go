package operations

import (
	"errors"
	"testing"
)

func TestEvaluateRuleThresholds(t *testing.T) {
	filesystem := alertRule{name: "filesystem_low_space", enabled: true, severity: severityWarning, thresholds: defaultThresholds("filesystem_low_space")}
	inputs := alertInputs{freeBytes: 1 << 40, totalBytes: 2 << 40, storageKnown: true}
	if evaluation := evaluateRule(filesystem, inputs); evaluation.firing {
		t.Fatalf("half a volume free should not fire: %+v", evaluation)
	}
	inputs.freeBytes = 1 << 32
	if evaluation := evaluateRule(filesystem, inputs); !evaluation.firing {
		t.Fatalf("little free space should fire: %+v", evaluation)
	}
	inputs.freeBytes = 100 << 30
	inputs.totalBytes = 2 << 40
	if evaluation := evaluateRule(filesystem, inputs); !evaluation.firing {
		t.Fatalf("free space below ten percent should fire: %+v", evaluation)
	}
	if evaluation := evaluateRule(filesystem, alertInputs{storageKnown: false}); evaluation.firing {
		t.Fatalf("unknown storage must not fire: %+v", evaluation)
	}

	provider := alertRule{name: "provider_failures", enabled: true, severity: severityWarning, thresholds: defaultThresholds("provider_failures")}
	if evaluation := evaluateRule(provider, alertInputs{providerInWindow: 3}); evaluation.firing {
		t.Fatalf("the threshold itself must not fire: %+v", evaluation)
	}
	if evaluation := evaluateRule(provider, alertInputs{providerInWindow: 4}); !evaluation.firing {
		t.Fatalf("failures above the threshold must fire: %+v", evaluation)
	}

	failures := alertRule{name: "download_import_failures", enabled: true, severity: severityWarning, thresholds: defaultThresholds("download_import_failures")}
	if evaluation := evaluateRule(failures, alertInputs{failuresInWindow: 9}); !evaluation.firing {
		t.Fatalf("combined failures above the threshold must fire: %+v", evaluation)
	}

	stuck := alertRule{name: "job_stuck", enabled: true, severity: severityWarning, thresholds: defaultThresholds("job_stuck")}
	if evaluation := evaluateRule(stuck, alertInputs{stuckJobs: 0}); evaluation.firing {
		t.Fatalf("no stuck jobs must not fire: %+v", evaluation)
	}
	if evaluation := evaluateRule(stuck, alertInputs{stuckJobs: 2}); !evaluation.firing {
		t.Fatalf("stuck jobs must fire: %+v", evaluation)
	}

	health := alertRule{name: "db_health", enabled: true, severity: severityCritical, thresholds: defaultThresholds("db_health")}
	if evaluation := evaluateRule(health, alertInputs{dbFailures: 1}); evaluation.firing {
		t.Fatalf("one failed check must not fire: %+v", evaluation)
	}
	if evaluation := evaluateRule(health, alertInputs{dbFailures: 2}); !evaluation.firing {
		t.Fatalf("repeated database failures must fire: %+v", evaluation)
	}
}

func TestThresholdsFromViewValidation(t *testing.T) {
	if _, err := thresholdsFromView("filesystem_low_space", ThresholdView{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero thresholds must be rejected, got %v", err)
	}
	if _, err := thresholdsFromView("filesystem_low_space", ThresholdView{MinimumFreePercent: 150}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an impossible percentage must be rejected, got %v", err)
	}
	if _, err := thresholdsFromView("provider_failures", ThresholdView{WindowMinutes: 0, Threshold: 2}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a zero window must be rejected, got %v", err)
	}
	thresholds, err := thresholdsFromView("job_stuck", ThresholdView{StuckMinutes: 30})
	if err != nil || thresholds.StuckMinutes == nil || *thresholds.StuckMinutes != 30 {
		t.Fatalf("a valid stuck threshold must be accepted: %+v (%v)", thresholds, err)
	}
	if _, err := thresholdsFromView("unknown_rule", ThresholdView{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown rule must be rejected, got %v", err)
	}
	if _, err := thresholdsFromView("db_health", ThresholdView{Failures: 0}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero failures must be rejected, got %v", err)
	}
}

func TestMergeThresholdsKeepsDefaults(t *testing.T) {
	merged := mergeThresholds(defaultThresholds("provider_failures"), []byte(`{"threshold": 10}`))
	if merged.Threshold == nil || *merged.Threshold != 10 {
		t.Fatalf("stored threshold was not applied: %+v", merged)
	}
	if merged.WindowMinutes == nil || *merged.WindowMinutes != 60 {
		t.Fatalf("missing defaults were not filled: %+v", merged)
	}
	merged = mergeThresholds(defaultThresholds("provider_failures"), []byte(`not json`))
	if merged.Threshold == nil || *merged.Threshold != 3 {
		t.Fatalf("unreadable config must fall back to defaults: %+v", merged)
	}
}
