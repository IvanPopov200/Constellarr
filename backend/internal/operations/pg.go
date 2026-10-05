package operations

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
)

const toolOutputLimit = 64 << 10

type pgTools struct {
	mu              sync.Mutex
	explicitDump    string
	explicitRestore string
	checked         bool
	dump            string
	restore         string
	err             error
}

func (s *Service) pgDumpPath() (string, error) {
	return s.toolPath(true)
}

func (s *Service) pgRestorePath() (string, error) {
	return s.toolPath(false)
}

func (s *Service) toolPath(dump bool) (string, error) {
	s.tools.mu.Lock()
	defer s.tools.mu.Unlock()
	if !s.tools.checked {
		s.tools.checked = true
		s.tools.dump, s.tools.err = resolveTool(s.tools.explicitDump, "PG_DUMP_PATH", "pg_dump")
		if s.tools.err == nil {
			s.tools.restore, s.tools.err = resolveTool(s.tools.explicitRestore, "PG_RESTORE_PATH", "pg_restore")
		}
	}
	if s.tools.err != nil {
		return "", s.tools.err
	}
	if dump {
		return s.tools.dump, nil
	}
	return s.tools.restore, nil
}

func resolveTool(explicit, envName, name string) (string, error) {
	path := strings.TrimSpace(explicit)
	if path == "" {
		path = strings.TrimSpace(os.Getenv(envName))
	}
	if path == "" {
		found, err := exec.LookPath(name)
		if err != nil {
			return "", errors.New("operations: " + name + " was not found; install or bundle the PostgreSQL 18 client tools")
		}
		return found, nil
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", errors.New("operations: " + name + " was not found at the configured path")
	}
	return path, nil
}

func toolMajor(ctx context.Context, path string) (int, error) {
	output, err := runTool(ctx, path, toolEnv(), "--version")
	if err != nil {
		return 0, errors.New("operations: the PostgreSQL client version could not be read")
	}
	versionPattern := regexp.MustCompile(`(\d+)(\.\d+)*`)
	match := versionPattern.FindStringSubmatch(output)
	if match == nil {
		return 0, errors.New("operations: the PostgreSQL client version could not be read")
	}
	major, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, errors.New("operations: the PostgreSQL client version could not be read")
	}
	return major, nil
}

// requireCompatibleTools refuses client tools older than the server, as PostgreSQL documents.
func (s *Service) requireCompatibleTools(ctx context.Context) error {
	dumpPath, err := s.pgDumpPath()
	if err != nil {
		return err
	}
	if _, err := s.pgRestorePath(); err != nil {
		return err
	}
	major, err := toolMajor(ctx, dumpPath)
	if err != nil {
		return err
	}
	serverMajor, err := s.serverMajor(ctx)
	if err != nil {
		return err
	}
	if major < serverMajor {
		return errors.New("operations: the PostgreSQL client tools are older than the database server; bundle matching tools")
	}
	return nil
}

func (s *Service) serverMajor(ctx context.Context) (int, error) {
	var raw string
	if err := s.pool.QueryRow(ctx, `SHOW server_version_num`).Scan(&raw); err != nil {
		return 0, dbError("server version", err)
	}
	number, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || number < 10000 {
		return 0, errors.New("operations: the database server version could not be read")
	}
	return number / 10000, nil
}

// pgEnv connects the tools through PG* variables so the connection string never appears in process arguments.
func (s *Service) pgEnv(rights string) ([]string, error) {
	config, err := parseConnection(s.databaseURL)
	if err != nil {
		return nil, ErrNotConfigured
	}
	env := []string{
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.TempDir(),
		"PATH=" + os.Getenv("PATH"),
		"LC_ALL=C",
		"PGAPPNAME=constellarr-" + rights,
		"PGCONNECT_TIMEOUT=10",
		"PGHOST=" + config.host,
		"PGPORT=" + config.port,
		"PGUSER=" + config.user,
		"PGDATABASE=" + config.database,
	}
	if config.password != "" {
		env = append(env, "PGPASSWORD="+config.password)
	}
	if config.sslmode != "" {
		env = append(env, "PGSSLMODE="+config.sslmode)
	}
	return env, nil
}

func toolEnv() []string {
	return []string{
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.TempDir(),
		"PATH=" + os.Getenv("PATH"),
		"LC_ALL=C",
	}
}

type connection struct {
	host, port, user, password, database, sslmode string
}

func parseConnection(raw string) (connection, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return connection{}, ErrNotConfigured
	}
	parsed, err := pgconn.ParseConfig(trimmed)
	if err != nil {
		return connection{}, ErrNotConfigured
	}
	config := connection{
		host: parsed.Host, port: strconv.Itoa(int(parsed.Port)), user: parsed.User,
		password: parsed.Password, database: parsed.Database, sslmode: sslModeFrom(trimmed),
	}
	if config.host == "" || config.user == "" || config.database == "" {
		return connection{}, ErrNotConfigured
	}
	return config, nil
}

// pgconn consumes sslmode into its TLS configuration, so the raw setting is read from the URL or keyword DSN.
func sslModeFrom(raw string) string {
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" {
		return parsed.Query().Get("sslmode")
	}
	for _, field := range strings.Fields(raw) {
		if key, value, ok := strings.Cut(field, "="); ok && strings.EqualFold(key, "sslmode") {
			return strings.ReplaceAll(strings.Trim(value, `'`), `\'`, `'`)
		}
	}
	return ""
}

// runTool captures bounded output and never returns raw tool errors to callers.
func runTool(ctx context.Context, path string, env []string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, path, args...)
	command.Env = env
	var output limitedOutput
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return output.String(), errors.New("the PostgreSQL client command timed out")
		}
		return output.String(), errors.New("the PostgreSQL client command failed")
	}
	return output.String(), nil
}

type limitedOutput struct {
	buffer strings.Builder
	limit  int
}

func (o *limitedOutput) Write(body []byte) (int, error) {
	limit := o.limit
	if limit == 0 {
		limit = toolOutputLimit
	}
	if o.buffer.Len() < limit {
		remaining := limit - o.buffer.Len()
		if len(body) > remaining {
			body = body[:remaining]
		}
		o.buffer.Write(body)
	}
	return len(body), nil
}

func (o *limitedOutput) String() string { return o.buffer.String() }

// sanitizeToolError removes connection details and keeps a short first line for people.
func sanitizeToolError(output, databaseURL string) string {
	cleaned := output
	if databaseURL != "" {
		cleaned = strings.ReplaceAll(cleaned, databaseURL, "[connection]")
	}
	if config, err := parseConnection(databaseURL); err == nil && config.password != "" {
		cleaned = strings.ReplaceAll(cleaned, config.password, "***")
	}
	lines := strings.Split(strings.TrimSpace(cleaned), "\n")
	if len(lines) > 3 {
		lines = lines[:3]
	}
	message := strings.TrimSpace(strings.Join(lines, " "))
	if message == "" {
		message = "no output"
	}
	return truncate(message, 300)
}

type tocInfo struct {
	entries       int
	hasDatabase   bool
	hasTablespace bool
	tables        map[string]bool
	serverVersion string
}

func (t tocInfo) hasTable(name string) bool { return t.tables[name] }

// parseTOC reads pg_restore --list output to reject cluster-level and unexpected dumps.
func parseTOC(output string) tocInfo {
	info := tocInfo{tables: map[string]bool{}}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, ";") {
			if index := strings.Index(trimmed, "Dumped from database version:"); index >= 0 {
				info.serverVersion = strings.TrimSpace(trimmed[index+len("Dumped from database version:"):])
			}
			continue
		}
		info.entries++
		fields := strings.Fields(trimmed)
		if len(fields) < 4 {
			continue
		}
		if containsSequence(fields, "DATABASE") {
			info.hasDatabase = true
		}
		if containsSequence(fields, "TABLESPACE") {
			info.hasTablespace = true
		}
		if containsSequence(fields, "TABLE") {
			name := fields[len(fields)-2]
			info.tables[name] = true
		}
	}
	return info
}

func containsSequence(fields []string, value string) bool {
	for _, field := range fields {
		if field == value {
			return true
		}
	}
	return false
}

var expectedTables = []string{"schema_migrations", "settings", "downloads", "operations_config"}

func validateTOC(info tocInfo) error {
	if info.hasDatabase || info.hasTablespace {
		return InvalidError("the dump contains cluster-level objects and cannot be restored")
	}
	if info.entries == 0 {
		return InvalidError("the dump is empty")
	}
	for _, table := range expectedTables {
		if info.hasTable(table) {
			return nil
		}
	}
	return InvalidError("the dump does not contain the expected Constellarr tables")
}

// writeFilesArchive stores configuration files from the data directory; media stays out of backups.
func (s *Service) writeFilesArchive(destination string) (int64, error) {
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, errors.New("operations: the backup file archive could not be created")
	}
	archive := tar.NewWriter(file)
	total := int64(0)
	appendFile := func(name string, info os.FileInfo) error {
		if !info.Mode().IsRegular() || info.Size() > maxConfigEntryBytes {
			return nil
		}
		source, err := os.Open(filepath.Join(s.dataDir, filepath.FromSlash(name)))
		if err != nil {
			return nil
		}
		defer source.Close()
		if err := archive.WriteHeader(&tar.Header{
			Name: name, Mode: 0o600, Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg,
		}); err != nil {
			return errors.New("operations: the backup file archive could not be written")
		}
		written, err := io.Copy(archive, io.LimitReader(source, maxConfigEntryBytes))
		if err != nil {
			return errors.New("operations: the backup file archive could not be written")
		}
		total += written
		if total > maxConfigArchiveTotal {
			return InvalidError("the configuration files are too large to back up")
		}
		return nil
	}
	entries, err := os.ReadDir(s.dataDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		archive.Close()
		file.Close()
		return 0, errors.New("operations: the data directory could not be read")
	}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case entry.IsDir() && name == "config":
			err = filepath.WalkDir(filepath.Join(s.dataDir, name), func(current string, item os.DirEntry, walkErr error) error {
				if walkErr != nil || item.IsDir() {
					return nil
				}
				relative, relErr := filepath.Rel(s.dataDir, current)
				if relErr != nil {
					return nil
				}
				info, infoErr := item.Info()
				if infoErr != nil {
					return nil
				}
				return appendFile(filepath.ToSlash(relative), info)
			})
			if err != nil {
				archive.Close()
				file.Close()
				return 0, err
			}
		case !entry.IsDir() && allowedConfigEntry(name):
			info, infoErr := entry.Info()
			if infoErr != nil {
				continue
			}
			if err = appendFile(name, info); err != nil {
				archive.Close()
				file.Close()
				return 0, err
			}
		}
	}
	if err := archive.Close(); err != nil {
		file.Close()
		return 0, errors.New("operations: the backup file archive could not be written")
	}
	if err := file.Close(); err != nil {
		return 0, errors.New("operations: the backup file archive could not be written")
	}
	return total, nil
}

func allowedConfigEntry(name string) bool {
	if strings.Contains(name, "/") {
		if !strings.HasPrefix(name, "config/") {
			return false
		}
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".json", ".yaml", ".yml", ".toml":
		return true
	}
	return false
}
