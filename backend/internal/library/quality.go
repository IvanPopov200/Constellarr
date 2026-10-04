package library

import "github.com/IvanPopov200/Constellarr/backend/internal/quality"

// parseQuality isolates the shared quality parser contract for release names.
func parseQuality(name string) string {
	return quality.Parse(name).Quality
}
