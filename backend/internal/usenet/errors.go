package usenet

import (
	"strings"
)

// providerError exposes a fixed message while keeping errors.Is working on the upstream cause.
type providerError struct {
	message string
	cause   error
}

func (e *providerError) Error() string { return e.message }
func (e *providerError) Unwrap() error { return e.cause }

// sanitizeProviderError keeps a hostile server's response text, which can echo credentials, out of public errors.
func sanitizeProviderError(prefix string, err error) error {
	return &providerError{message: prefix + providerFailure(err), cause: err}
}

func providerFailure(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "tls") || strings.Contains(msg, "x509") || strings.Contains(msg, "certificate"):
		return "TLS handshake failed (check the host name and certificate)"
	case strings.Contains(msg, "auth"):
		return "authentication failed (check the username and password)"
	case strings.Contains(msg, "quota"):
		return "provider quota exceeded"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out") || strings.Contains(msg, "deadline exceeded"):
		return "provider did not respond in time"
	case strings.Contains(msg, "dial") || strings.Contains(msg, "connect") || strings.Contains(msg, "network") ||
		strings.Contains(msg, "refused") || strings.Contains(msg, "reset") || strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "eof") || strings.Contains(msg, "greeting"):
		return "could not reach the provider (check the host, port and network)"
	default:
		return "provider request failed"
	}
}

// providerErr names the host only when a single host makes the attribution unambiguous.
func providerErr(hosts []string, err error) error {
	prefix := ""
	if len(hosts) == 1 {
		prefix = hosts[0] + ": "
	}
	return sanitizeProviderError(prefix, err)
}
