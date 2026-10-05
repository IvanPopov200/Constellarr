package discovery

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxNameLookups = 250
	maxNameRunes   = 80
	systemName     = "System"
	deletedName    = "Deleted user"
	unknownName    = "Unknown user"
)

// nameResolver memoises display names for one response and bounds how often the hook runs.
type nameResolver struct {
	lookup   UserName
	names    map[string]string
	requests int
}

func newNameResolver(lookup UserName) *nameResolver {
	return &nameResolver{lookup: lookup, names: map[string]string{}}
}

// name resolves one account ID; it never exposes the ID when a name cannot be found.
func (r *nameResolver) name(ctx context.Context, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if known, ok := r.names[id]; ok {
		return known
	}
	// Automation writes this actor for its own transitions.
	if id == "system" {
		r.names[id] = systemName
		return systemName
	}
	if r.lookup == nil || r.requests >= maxNameLookups {
		r.names[id] = unknownName
		return unknownName
	}
	r.requests++
	resolved := cleanName(r.lookup(ctx, id))
	if resolved == "" {
		resolved = deletedName
	}
	r.names[id] = resolved
	return resolved
}

func cleanName(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) > maxNameRunes {
		value = string([]rune(value)[:maxNameRunes])
	}
	return value
}

// namesFor attaches display names to one request.
func (s *Service) namesFor(ctx context.Context, resolver *nameResolver, request *Request) {
	request.UserName = resolver.name(ctx, request.UserID)
	request.DecidedByName = resolver.name(ctx, request.DecidedBy)
}
