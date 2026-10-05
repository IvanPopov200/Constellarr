package torrents

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/IvanPopov200/Constellarr/backend/internal/media"
)

const (
	processPending   = "pending"
	processRunning   = "running"
	processCompleted = "completed"
	processFailed    = "failed"
	processSkipped   = "skipped"

	processingConcurrent = 1
	processingBatch      = 25
	processedDirName     = "processed"
)

// mediaExtensions mirrors the playable formats accepted by the shared media extractor.
var mediaExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".mov": true, ".m4v": true, ".webm": true,
	".mpeg": true, ".mpg": true, ".ts": true, ".wmv": true, ".flac": true, ".mp3": true,
	".m4a": true, ".alac": true, ".aac": true, ".ogg": true, ".opus": true, ".wav": true,
	".aiff": true, ".ape": true, ".wv": true,
}

var extractableExtensions = map[string]bool{".rar": true, ".zip": true, ".r00": true}

var unsupportedArchiveExtensions = map[string]bool{
	".7z": true, ".tar": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true,
	".iso": true, ".img": true, ".cab": true, ".zst": true, ".001": true,
}

// Processing reports archive extraction state for a completed torrent.
type Processing struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type processingRow struct {
	jobID string
	state string
	error string
	input string
	files []File
}

func (r processingRow) public() *Processing {
	if r.state == "" {
		return nil
	}
	return &Processing{State: r.state, Error: r.error}
}

// hasImportableMedia reports whether the payload already contains files the importers can use.
func hasImportableMedia(files []File) bool {
	for _, file := range files {
		if mediaExtensions[strings.ToLower(path.Ext(file.Name))] {
			return true
		}
	}
	return false
}

// archiveInputRoot returns the payload subdirectory holding the archives, if any.
func archiveInputRoot(files []File) (string, bool) {
	dirs := make(map[string]bool)
	for _, file := range files {
		if extractableExtensions[strings.ToLower(path.Ext(file.Name))] {
			dirs[path.Dir(file.Name)] = true
		}
	}
	if len(dirs) == 0 {
		return "", false
	}
	prefix, first := "", true
	for dir := range dirs {
		if first {
			prefix, first = dir, false
			continue
		}
		prefix = commonPathPrefix(prefix, dir)
	}
	if prefix == "" || prefix == "." {
		return "", true
	}
	clean, err := splitRel(prefix)
	if err != nil {
		return "", true
	}
	return clean, true
}

func commonPathPrefix(a, b string) string {
	left := strings.Split(a, "/")
	right := strings.Split(b, "/")
	shared := make([]string, 0, len(left))
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] != right[i] {
			break
		}
		shared = append(shared, left[i])
	}
	return strings.Join(shared, "/")
}

func hasUnsupportedArchive(files []File) bool {
	for _, file := range files {
		if unsupportedArchiveExtensions[strings.ToLower(path.Ext(file.Name))] {
			return true
		}
	}
	return false
}

// processingDecision picks the durable state a completed payload starts in.
func processingDecision(files []File) processingRow {
	if hasImportableMedia(files) {
		return processingRow{state: processSkipped}
	}
	if root, ok := archiveInputRoot(files); ok {
		return processingRow{state: processPending, input: root}
	}
	if hasUnsupportedArchive(files) {
		return processingRow{state: processFailed,
			error: "the download contains only archives that cannot be extracted automatically"}
	}
	return processingRow{state: processSkipped}
}

func (s *Service) processedDir(infoHash string) (string, error) {
	if !validInfoHash(infoHash) {
		return "", errUnsafePath
	}
	return filepath.Join(s.root, processedDirName, infoHash), nil
}

func (s *Service) loadProcessing(ctx context.Context, q querier, jobID string) (processingRow, error) {
	row := processingRow{jobID: jobID}
	var files []byte
	err := q.QueryRow(ctx, `SELECT state, error, input_root, files FROM torrent_processing WHERE job_id = $1`, jobID).
		Scan(&row.state, &row.error, &row.input, &files)
	if errors.Is(err, pgx.ErrNoRows) {
		return processingRow{}, ErrNotFound
	}
	if err != nil {
		return processingRow{}, storeError("load processing state", err)
	}
	row.files = decodeProcessedFiles(files)
	return row, nil
}

func decodeProcessedFiles(raw []byte) []File {
	files := make([]File, 0)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &files); err != nil {
			files = []File{}
		}
	}
	return files
}

func (s *Service) processingByJob(ctx context.Context, q querier) (map[string]processingRow, error) {
	rows, err := q.Query(ctx, `SELECT job_id, state, error, input_root, files FROM torrent_processing`)
	if err != nil {
		return nil, storeError("list processing state", err)
	}
	defer rows.Close()
	states := make(map[string]processingRow)
	for rows.Next() {
		row := processingRow{}
		var files []byte
		if err := rows.Scan(&row.jobID, &row.state, &row.error, &row.input, &files); err != nil {
			return nil, storeError("list processing state", err)
		}
		row.files = decodeProcessedFiles(files)
		states[row.jobID] = row
	}
	if err := rows.Err(); err != nil {
		return nil, storeError("list processing state", err)
	}
	return states, nil
}

func (s *Service) saveProcessing(ctx context.Context, q querier, row processingRow) error {
	files, err := json.Marshal(row.files)
	if err != nil {
		return errors.New("torrents: the extracted file list could not be encoded")
	}
	_, err = q.Exec(ctx, `INSERT INTO torrent_processing (job_id, state, error, input_root, files)
		VALUES ($1, $2, $3, $4, $5::jsonb)
		ON CONFLICT (job_id) DO UPDATE SET state = EXCLUDED.state, error = EXCLUDED.error,
		input_root = EXCLUDED.input_root, files = EXCLUDED.files, updated_at = now()`,
		row.jobID, row.state, row.error, row.input, string(files))
	if err != nil {
		return storeError("save processing state", err)
	}
	return nil
}

func (s *Service) deleteProcessing(ctx context.Context, q querier, jobID string) error {
	if _, err := q.Exec(ctx, `DELETE FROM torrent_processing WHERE job_id = $1`, jobID); err != nil {
		return storeError("delete processing state", err)
	}
	return nil
}

// resumeProcessing returns interrupted extractions to the queue after a restart.
func (s *Service) resumeProcessing(ctx context.Context, q querier) error {
	_, err := q.Exec(ctx, `UPDATE torrent_processing SET state = $1, updated_at = now() WHERE state = $2`,
		processPending, processRunning)
	if err != nil {
		return storeError("resume processing", err)
	}
	return nil
}

// discoverProcessing records a decision for completed payloads that have no state yet.
func (s *Service) discoverProcessing(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT j.id, j.files FROM torrent_jobs j
		LEFT JOIN torrent_processing p ON p.job_id = j.id
		WHERE p.job_id IS NULL AND j.status IN ($1, $2) ORDER BY j.added_at LIMIT $3`,
		statusSeeding, statusCompleted, processingBatch)
	if err != nil {
		return storeError("discover processing", err)
	}
	candidates := make([]struct {
		id    string
		files []File
	}, 0, processingBatch)
	for rows.Next() {
		var (
			id    string
			files []byte
		)
		if err := rows.Scan(&id, &files); err != nil {
			rows.Close()
			return storeError("discover processing", err)
		}
		candidates = append(candidates, struct {
			id    string
			files []File
		}{id: id, files: decodeProcessedFiles(files)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return storeError("discover processing", err)
	}
	for _, candidate := range candidates {
		row := processingDecision(candidate.files)
		row.jobID = candidate.id
		if err := s.saveProcessing(ctx, s.pool, row); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) pendingProcessing(ctx context.Context) ([]processingRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT job_id, state, error, input_root FROM torrent_processing
		WHERE state = $1 ORDER BY updated_at LIMIT $2`, processPending, processingConcurrent)
	if err != nil {
		return nil, storeError("list pending processing", err)
	}
	defer rows.Close()
	pending := make([]processingRow, 0, processingConcurrent)
	for rows.Next() {
		row := processingRow{}
		if err := rows.Scan(&row.jobID, &row.state, &row.error, &row.input); err != nil {
			return nil, storeError("list pending processing", err)
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		return nil, storeError("list pending processing", err)
	}
	return pending, nil
}

func (s *Service) countProcessing(ctx context.Context) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM torrent_processing WHERE state IN ($1, $2)`,
		processPending, processRunning).Scan(&count)
	if err != nil {
		return 0, storeError("count processing", err)
	}
	return count, nil
}

// scheduleProcessing queues and starts bounded archive extraction outside request handlers.
func (s *Service) scheduleProcessing(ctx context.Context) {
	if err := s.discoverProcessing(ctx); err != nil {
		s.setEngineError(err.Error())
	}
	if s.processingActive.Load() >= processingConcurrent {
		return
	}
	pending, err := s.pendingProcessing(ctx)
	if err != nil {
		s.setEngineError(err.Error())
		return
	}
	for _, row := range pending {
		if s.processingActive.Load() >= processingConcurrent {
			return
		}
		s.processingActive.Add(1)
		s.workers.Add(1)
		go func(row processingRow) {
			defer s.workers.Done()
			defer s.processingActive.Add(-1)
			s.runProcessing(ctx, row)
		}(row)
	}
}

func (s *Service) runProcessing(ctx context.Context, row processingRow) {
	job, err := s.jobByID(ctx, s.pool, row.jobID)
	if err != nil {
		return
	}
	payloadDir, err := s.dataDir(job.InfoHash)
	if err != nil {
		s.failProcessing(ctx, row.jobID, "the download directory is not valid")
		return
	}
	outDir, err := s.processedDir(job.InfoHash)
	if err != nil {
		s.failProcessing(ctx, row.jobID, "the extraction directory is not valid")
		return
	}
	inputDir := payloadDir
	if row.input != "" {
		inputDir = filepath.Join(payloadDir, filepath.FromSlash(row.input))
	}
	if err := s.prepareProcessedDir(outDir); err != nil {
		s.failProcessing(ctx, row.jobID, "the extraction directory could not be prepared")
		return
	}
	row.state = processRunning
	if err := s.saveProcessing(ctx, s.pool, row); err != nil {
		s.setEngineError(err.Error())
		return
	}
	files, err := s.extractPayload(ctx, inputDir, outDir)
	if err != nil {
		if ctx.Err() != nil {
			s.requeueProcessing(row.jobID)
			return
		}
		s.failProcessing(ctx, row.jobID, sanitizeProcessError(err, payloadDir, outDir))
		return
	}
	row.state, row.error, row.files = processCompleted, "", files
	if err := s.saveProcessing(ctx, s.pool, row); err != nil {
		s.setEngineError(err.Error())
	}
}

// processMedia runs the shared extraction pipeline; tests replace it to control timing and failures.
var processMedia = media.Process

// extractPayload runs the shared media pipeline, which extracts archives and copies small sidecars.
func (s *Service) extractPayload(ctx context.Context, inputDir, outDir string) ([]File, error) {
	processed, err := processMedia(ctx, inputDir, outDir, 0, nil)
	if err != nil {
		return nil, err
	}
	files := make([]File, 0, len(processed))
	for _, file := range processed {
		name, err := splitRel(file.Name)
		if err != nil || file.Size < 0 {
			return nil, errUnsafePath
		}
		files = append(files, File{Name: name, Size: file.Size})
	}
	if len(files) == 0 {
		return nil, errors.New("no playable media found in the download")
	}
	return files, nil
}

func (s *Service) prepareProcessedDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return checkNoSymlinks(dir)
}

func (s *Service) failProcessing(ctx context.Context, jobID, message string) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	if err := s.saveProcessing(writeCtx, s.pool, processingRow{jobID: jobID, state: processFailed, error: message}); err != nil {
		s.setEngineError(err.Error())
	}
}

// requeueProcessing keeps an interrupted extraction on the durable queue for the next start.
func (s *Service) requeueProcessing(jobID string) {
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	if err := s.saveProcessing(ctx, s.pool, processingRow{jobID: jobID, state: processPending}); err != nil {
		s.setEngineError(err.Error())
	}
}

// retryProcessing lets an explicit resume retry a failed extraction.
func (s *Service) retryProcessing(ctx context.Context, jobID string) bool {
	row, err := s.loadProcessing(ctx, s.pool, jobID)
	if err != nil || row.state != processFailed {
		return false
	}
	row.state, row.error = processPending, ""
	if err := s.saveProcessing(ctx, s.pool, row); err != nil {
		s.setEngineError(err.Error())
		return false
	}
	s.wakeEngine()
	return true
}

func (s *Service) removeProcessedDir(infoHash string) error {
	dir, err := s.processedDir(infoHash)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return errors.New("torrents: the extraction directory could not be removed")
	}
	return nil
}

// sanitizeProcessError keeps extraction failures free of absolute paths.
func sanitizeProcessError(err error, dirs ...string) string {
	message := err.Error()
	sort.SliceStable(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if dir != "" {
			message = strings.ReplaceAll(message, dir, "...")
		}
	}
	message = strings.TrimSpace(strings.Join(strings.Fields(message), " "))
	if message == "" {
		message = "the download could not be extracted"
	}
	if utf8.RuneCountInString(message) > maxNameRunes {
		message = string([]rune(message)[:maxNameRunes])
	}
	return message
}
