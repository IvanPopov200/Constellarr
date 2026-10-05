package usenet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/javi11/nntppool/v5"
)

const (
	providerAttemptTimeout = 25 * time.Second
	providerStallTimeout   = 25 * time.Second
	testTimeout            = 30 * time.Second
	MaxFallbackHosts       = 8
)

type Config struct {
	Host          string
	Port          int
	Username      string
	Password      string
	Connections   int
	FallbackHosts []string
}

type Progress struct {
	DownloadedBytes   int64
	TotalBytes        int64
	CompletedSegments int
	TotalSegments     int
	MissingSegments   int
}

type Result struct {
	Files           []string
	MissingSegments int
}

// tlsRootCAs is set only by tests to trust a local CA.
var tlsRootCAs *x509.CertPool

func tlsConfigFor(host string) *tls.Config {
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, RootCAs: tlsRootCAs}
}

// Test never fetches an article.
func Test(ctx context.Context, cfg Config) error {
	hosts, err := cfg.hosts()
	if err != nil {
		return err
	}
	for _, host := range hosts {
		provider := providerFor(host, cfg)
		tctx, cancel := context.WithTimeout(ctx, testTimeout)
		res := nntppool.TestProvider(tctx, provider)
		cancel()
		if res.Err != nil {
			return fmt.Errorf("usenet: %s: %w", host, sanitizeProviderError("", res.Err))
		}
	}
	return nil
}

func Download(ctx context.Context, nzb []byte, dir string, cfg Config, onProgress func(Progress)) (Result, error) {
	files, err := parseNZB(nzb)
	if err != nil {
		return Result{}, fmt.Errorf("usenet: %w", err)
	}
	hosts, err := cfg.hosts()
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("usenet: create download dir: %w", err)
	}
	partsDir := filepath.Join(dir, cacheDirName)
	if err := os.MkdirAll(partsDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("usenet: create cache dir: %w", err)
	}

	state := &progressState{}
	wanted := make(map[string]struct{})
	var tasks []segmentTask
	for i := range files {
		for _, s := range files[i].segments {
			state.totalBytes += s.bytes
			state.totalSegments++
			tasks = append(tasks, segmentTask{file: i, messageID: s.messageID, number: s.number})
			wanted[s.messageID] = struct{}{}
		}
	}
	emit(onProgress, state.snapshot())

	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()
	clients, err := newClients(jobCtx, cfg, hosts)
	if err != nil {
		return Result{}, err
	}
	defer closeClients(clients)

	cached := loadCachedSegments(partsDir, wanted)
	states := make([]*fileState, len(files))
	for i := range files {
		states[i] = &fileState{index: i, nzb: &files[i]}
	}

	taskCh := make(chan segmentTask)
	resultCh := make(chan segmentResult)
	var wg sync.WaitGroup
	for i := 0; i < connectionCount(cfg); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker(jobCtx, clients, partsDir, cached, taskCh, resultCh)
		}()
	}
	go func() {
		defer close(taskCh)
		for _, task := range tasks {
			select {
			case taskCh <- task:
			case <-jobCtx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var (
		hardErr    error
		damagedErr error
		fatal      error
	)
	for res := range resultCh {
		st := states[res.task.file]
		if res.err != nil {
			if res.hard {
				if hardErr == nil {
					hardErr = res.err
				}
			} else if damagedErr == nil {
				damagedErr = res.err
			}
			if res.fatal && fatal == nil {
				fatal = res.err
				jobCancel()
			}
			st.missing++
			state.missing++
			emit(onProgress, state.snapshot())
			continue
		}
		if err := st.accept(res.seg); err != nil {
			if damagedErr == nil {
				damagedErr = err
			}
			st.missing++
			state.missing++
			emit(onProgress, state.snapshot())
			continue
		}
		state.completed++
		state.downloaded += res.seg.PartSize
		emit(onProgress, state.snapshot())
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, ctxErr
	}
	if fatal != nil {
		return Result{MissingSegments: state.missing}, fmt.Errorf("usenet: %w", fatal)
	}
	if state.completed == 0 {
		if hardErr != nil {
			return Result{MissingSegments: state.missing}, fmt.Errorf("usenet: no segments could be downloaded: %w", providerErr(hosts, hardErr))
		}
		if damagedErr != nil {
			return Result{MissingSegments: state.missing}, fmt.Errorf("usenet: no usable segments: %w", damagedErr)
		}
		return Result{MissingSegments: state.missing}, fmt.Errorf("usenet: none of the %d segments could be downloaded", state.totalSegments)
	}

	result, err := assembleAll(dir, partsDir, states, state)
	if err != nil {
		return result, err
	}
	if hardErr != nil {
		return result, fmt.Errorf("usenet: %d of %d segments failed: %w", state.missing, state.totalSegments, providerErr(hosts, hardErr))
	}
	return result, nil
}

func assembleAll(dir, partsDir string, states []*fileState, state *progressState) (Result, error) {
	names := make(map[string]int, len(states))
	var (
		result  Result
		failed  []string
		reasons []string
	)
	for _, st := range states {
		if st.size == 0 {
			continue
		}
		key := strings.ToLower(st.name)
		if prev, dup := names[key]; dup {
			return result, fmt.Errorf("usenet: files %d and %d both assemble to %q", prev+1, st.index+1, st.name)
		}
		names[key] = st.index
		path, err := assembleFile(dir, partsDir, st)
		if err != nil {
			failed = append(failed, st.name)
			reasons = append(reasons, err.Error())
			continue
		}
		result.Files = append(result.Files, path)
	}
	result.MissingSegments = state.missing
	if len(failed) > 0 {
		return result, fmt.Errorf("usenet: %d file(s) failed to assemble (%s): %s", len(failed), strings.Join(failed, ", "), strings.Join(reasons, "; "))
	}
	return result, nil
}

// newClients gives each host its own lazy single-provider pool so configured host order is enforced here.
func newClients(ctx context.Context, cfg Config, hosts []string) ([]*nntppool.Client, error) {
	clients := make([]*nntppool.Client, 0, len(hosts))
	for _, host := range hosts {
		client, err := nntppool.NewClient(ctx, []nntppool.Provider{providerFor(host, cfg)}, nntppool.WithStatProbe(false))
		if err != nil {
			closeClients(clients)
			return nil, fmt.Errorf("usenet: %w", err)
		}
		clients = append(clients, client)
	}
	return clients, nil
}

func closeClients(clients []*nntppool.Client) {
	for _, client := range clients {
		_ = client.Close()
	}
}

func providerFor(host string, cfg Config) nntppool.Provider {
	return nntppool.Provider{
		Name:           host,
		Host:           net.JoinHostPort(host, strconv.Itoa(cfg.Port)),
		TLSConfig:      tlsConfigFor(host),
		Auth:           nntppool.Auth{Username: cfg.Username, Password: cfg.Password},
		Connections:    connectionCount(cfg),
		SkipPing:       true,
		AttemptTimeout: providerAttemptTimeout,
		StallTimeout:   providerStallTimeout,
		UserAgent:      "Constellarr",
	}
}

func connectionCount(cfg Config) int {
	if cfg.Connections < 1 {
		return 1
	}
	if cfg.Connections > 32 {
		return 32
	}
	return cfg.Connections
}

func (c Config) hosts() ([]string, error) {
	host, err := ValidateHost(c.Host)
	if err != nil {
		return nil, err
	}
	if c.Port < 1 || c.Port > 65535 {
		return nil, fmt.Errorf("usenet: port %d is out of range", c.Port)
	}
	if c.Username != "" && c.Password == "" {
		return nil, errors.New("usenet: password is required when a username is set")
	}
	hosts := []string{host}
	seen := map[string]bool{strings.ToLower(host): true}
	for _, raw := range c.FallbackHosts {
		fallback, err := ValidateHost(raw)
		if err != nil || seen[strings.ToLower(fallback)] {
			continue
		}
		if len(hosts) > MaxFallbackHosts {
			return nil, fmt.Errorf("usenet: at most %d fallback hosts are supported", MaxFallbackHosts)
		}
		seen[strings.ToLower(fallback)] = true
		hosts = append(hosts, fallback)
	}
	return hosts, nil
}

func ValidateHost(host string) (string, error) {
	if strings.TrimSpace(host) == "" {
		return "", errors.New("usenet: host is required")
	}
	clean := cleanHost(host)
	if clean == "" {
		return "", errors.New("usenet: invalid host; enter a bare hostname")
	}
	return clean, nil
}

func cleanHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	if strings.ContainsAny(host, "/@ \t\r\n") {
		return ""
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return ""
	}
	return host
}

type progressState struct {
	downloaded    int64
	totalBytes    int64
	completed     int
	totalSegments int
	missing       int
}

func (p *progressState) snapshot() Progress {
	return Progress{
		DownloadedBytes:   p.downloaded,
		TotalBytes:        p.totalBytes,
		CompletedSegments: p.completed,
		TotalSegments:     p.totalSegments,
		MissingSegments:   p.missing,
	}
}

// emit runs on the coordinator goroutine only, so callbacks are serial.
func emit(onProgress func(Progress), p Progress) {
	if onProgress != nil {
		onProgress(p)
	}
}
