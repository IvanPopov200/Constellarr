package quality

import (
	"fmt"
	"regexp"
	"strconv"
)

// Rank is the release quality position in Profile.Qualities, where 0 is most preferred.
func Evaluate(profile Profile, title string, size int64, current *Current) Decision {
	details := Parse(title)
	decision := Decision{Details: details, Rank: -1}
	reasons := make([]string, 0, 2)
	rank, allowed := profileRank(profile, details.Quality)
	if allowed {
		decision.Rank = rank
	}

	switch {
	case details.Quality == "":
		reasons = append(reasons, "quality is unknown")
	case !isSelectable(details.Quality):
		reasons = append(reasons, "quality "+details.Quality+" is never selected automatically")
	case !allowed:
		reasons = append(reasons, "quality "+details.Quality+" is not in the profile")
	}

	decision.Score = ruleScore(profile.Rules, title, &reasons)
	if decision.Score < profile.MinScore {
		reasons = append(reasons, fmt.Sprintf("score %d is below the minimum %d", decision.Score, profile.MinScore))
	}

	if profile.Language != "" && !languageSatisfied(normalizeLanguage(profile.Language), details.Language) {
		reasons = append(reasons, "release does not declare language "+profile.Language)
	}

	sizeMB := float64(size) / (1 << 20)
	if profile.MinMB > 0 && sizeMB < profile.MinMB {
		reasons = append(reasons, fmt.Sprintf("size %.0f MB is below the minimum %.0f MB", sizeMB, profile.MinMB))
	}
	if profile.MaxMB > 0 && sizeMB > profile.MaxMB {
		reasons = append(reasons, fmt.Sprintf("size %.0f MB is above the maximum %.0f MB", sizeMB, profile.MaxMB))
	}

	if current != nil {
		evaluateUpgrade(&decision, profile, *current, allowed, &reasons)
	}

	decision.Reasons = reasons
	decision.Allowed = len(reasons) == 0
	return decision
}

func evaluateUpgrade(decision *Decision, profile Profile, current Current, allowed bool, reasons *[]string) {
	switch {
	case Satisfied(profile, current):
		*reasons = append(*reasons, "current release already meets the profile cutoff")
	case !profile.Upgrade:
		*reasons = append(*reasons, "upgrades are disabled for this profile")
	case !allowed:
		// The release is already rejected, so no upgrade comparison applies.
	default:
		currentRank, currentAllowed := profileRank(profile, current.Quality)
		switch {
		case currentAllowed && currentRank < decision.Rank:
			*reasons = append(*reasons, "release is a downgrade in the profile order")
		case currentAllowed && currentRank == decision.Rank && decision.Score <= current.Score:
			*reasons = append(*reasons, "release does not improve on the current score")
		case currentAllowed:
			decision.Upgrade = true
		case resolutionOf(current.Quality) > 0 && resolutionOf(decision.Details.Quality) < resolutionOf(current.Quality):
			// Never replace a file outside the profile with a lower global resolution.
			*reasons = append(*reasons, "release downgrades the current resolution from "+current.Quality)
		default:
			decision.Upgrade = true
		}
	}
}

func profileRank(profile Profile, id string) (int, bool) {
	for rank, quality := range profile.Qualities {
		if quality == id {
			return rank, true
		}
	}
	return -1, false
}

func isSelectable(id string) bool {
	_, ok := rankOf(id)
	return ok
}

// ruleScore sums matching rule scores; Negate rejects a match and Required rejects a miss.
func ruleScore(rules []Rule, title string, reasons *[]string) int {
	score := 0
	for i, rule := range rules {
		expression, err := regexp.Compile("(?i)" + rule.Pattern)
		if err != nil {
			continue
		}
		matched := expression.MatchString(title)
		if matched {
			score += rule.Score
		}
		switch {
		case rule.Negate && matched:
			*reasons = append(*reasons, "rejected by rule "+ruleLabel(rule, i))
		case !rule.Negate && rule.Required && !matched:
			*reasons = append(*reasons, "required rule "+ruleLabel(rule, i)+" did not match")
		}
	}
	return score
}

func ruleLabel(rule Rule, index int) string {
	if rule.Name != "" {
		return strconv.Quote(rule.Name)
	}
	return "rule " + strconv.Itoa(index+1)
}

func languageSatisfied(want, got string) bool {
	return want != "" && (got == want || got == "multi")
}
