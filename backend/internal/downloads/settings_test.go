package downloads

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/usenet"
)

func TestSafeErrorUsesOperationSnapshot(t *testing.T) {
	used := Config{APIKey: "old-key", Usenet: usenet.Config{Username: "old-user", Password: "old-password"}}
	message := safeError(errors.New("transfer failed for old-key with old-user/old-password"), used)
	for _, secret := range []string{"old-key", "old-user", "old-password"} {
		if strings.Contains(message, secret) {
			t.Fatalf("operation error leaks %q: %s", secret, message)
		}
	}
	if !strings.Contains(message, "[redacted]") {
		t.Fatalf("redaction marker missing: %s", message)
	}
}

func TestConfigSnapshotDuringRuntimeSwap(t *testing.T) {
	manager := &Manager{cfg: Config{Usenet: usenet.Config{Password: "first"}}}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if cfg := manager.Config(); cfg.Usenet.Password == "" {
					t.Error("Config returned an empty snapshot")
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 100; j++ {
			manager.configMu.Lock()
			manager.cfg.Usenet.Password = fmt.Sprintf("secret-%d", j)
			manager.configMu.Unlock()
		}
	}()
	wg.Wait()
}
