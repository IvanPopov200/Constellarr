package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/library"
)

// job payloads are bounded JSON documents stored with each durable job.
type downloadPayload struct {
	Request DownloadRequest `json:"request"`
}

type syncPayload struct {
	Request SyncRequest `json:"request"`
}

type translatePayload struct {
	Path           string `json:"path"`
	Language       string `json:"language"`
	SourceLanguage string `json:"sourceLanguage"`
}

type extractPayload struct {
	StreamIndex int    `json:"streamIndex"`
	Language    string `json:"language"`
	Preview     bool   `json:"preview"`
	Forced      *bool  `json:"forced"`
	HI          *bool  `json:"hi"`
}

func (s *Service) resolveVideo(ctx context.Context, kind, id string) (Video, error) {
	video, err := s.catalog.Video(ctx, kind, id)
	if err != nil {
		return Video{}, err
	}
	if err := verifyVideoFile(video); err != nil {
		return Video{}, err
	}
	return video, nil
}

// wantedPreferences returns the profile variants for one video, in profile order.
func (s *Service) wantedPreferences(ctx context.Context, cfg Config, video Video) ([]LanguagePreference, error) {
	profiles, err := s.store.Profiles(ctx)
	if err != nil {
		return nil, err
	}
	assignments, err := s.store.Assignments(ctx)
	if err != nil {
		return nil, err
	}
	profileID := assignments[videoKey(video.Kind, video.ID)].ProfileID
	if profileID == "" {
		profileID = cfg.DefaultProfileID
	}
	profile, ok := findProfile(profiles, profileID)
	if !ok && len(profiles) > 0 {
		profile = profiles[0]
	}
	return profileVariants(profile), nil
}

// Search finds provider candidates for the requested variants, or for the video's wanted languages.
func (s *Service) Search(ctx context.Context, kind, id string, prefs []LanguagePreference) (SearchOutcome, error) {
	video, err := s.resolveVideo(ctx, kind, id)
	if err != nil {
		return SearchOutcome{}, err
	}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return SearchOutcome{}, err
	}
	if len(prefs) == 0 {
		if prefs, err = s.wantedPreferences(ctx, cfg, video); err != nil {
			return SearchOutcome{}, err
		}
	}
	if len(prefs) == 0 {
		return SearchOutcome{}, fmt.Errorf("%w: no languages are configured for this video", ErrInvalid)
	}
	if len(prefs) > maxProfileLanguages {
		return SearchOutcome{}, fmt.Errorf("%w: at most %d languages can be searched at once", ErrInvalid, maxProfileLanguages)
	}
	for i, pref := range prefs {
		language, ok := NormalizeLanguage(pref.Code)
		if !ok {
			return SearchOutcome{}, fmt.Errorf("%w: unsupported language code %q", ErrInvalid, pref.Code)
		}
		prefs[i].Code = language
	}
	return s.searchVariants(ctx, cfg, video, prefs), nil
}

func (s *Service) searchVariants(ctx context.Context, cfg Config, video Video, prefs []LanguagePreference) SearchOutcome {
	outcome := SearchOutcome{Results: []Result{}, Warnings: []string{}}
	providers := s.configuredProviders(cfg)
	if len(providers) == 0 {
		outcome.Warnings = append(outcome.Warnings, "no subtitle provider is enabled and configured")
		return outcome
	}
	languages := make([]string, 0, len(prefs))
	for _, pref := range prefs {
		languages = append(languages, pref.Code)
	}
	timeout := time.Duration(clamp(cfg.ProviderTimeoutSeconds, 5, 120, defaultProviderSecs)) * time.Second
	for _, provider := range providers {
		providerCtx, cancel := context.WithTimeout(ctx, timeout)
		results, err := provider.Search(providerCtx, searchQuery(video, languages, maxSearchResults))
		cancel()
		if err != nil {
			message := publicError(err)
			outcome.Warnings = append(outcome.Warnings, provider.Name()+": "+message)
			s.noteProviderError(provider.ID(), message, 0, "")
			continue
		}
		for _, result := range results {
			best, matched := scoreAgainstPrefs(prefs, result)
			if !matched {
				continue
			}
			result.Score = best
			if language, ok := NormalizeLanguage(result.Language); ok {
				result.Language = language
			}
			outcome.Results = append(outcome.Results, result)
		}
	}
	sort.SliceStable(outcome.Results, func(i, j int) bool {
		if outcome.Results[i].Score != outcome.Results[j].Score {
			return outcome.Results[i].Score > outcome.Results[j].Score
		}
		if outcome.Results[i].Downloads != outcome.Results[j].Downloads {
			return outcome.Results[i].Downloads > outcome.Results[j].Downloads
		}
		return outcome.Results[i].FileID < outcome.Results[j].FileID
	})
	seen := map[string]bool{}
	deduped := make([]Result, 0, len(outcome.Results))
	for _, result := range outcome.Results {
		key := result.ProviderID + "/" + result.FileID
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, result)
		if len(deduped) >= maxSearchResults {
			break
		}
	}
	outcome.Results = deduped
	return outcome
}

func scoreAgainstPrefs(prefs []LanguagePreference, result Result) (int, bool) {
	best, matched := -1, false
	for _, pref := range prefs {
		if score := scoreResult(pref, result); score > best {
			best, matched = score, true
		}
	}
	return best, matched
}

func searchQuery(video Video, languages []string, limit int) SearchQuery {
	query := SearchQuery{Languages: languages, Limit: limit, Query: video.Title, Year: video.Year}
	switch video.Kind {
	case KindEpisode:
		query.Type = KindEpisode
		query.ParentIMDbID = firstNonEmpty(video.SeriesIMDbID, video.IMDbID)
		query.Season, query.Episode = video.Season, video.Episode
		if video.SeriesTitle != "" {
			query.Query = video.SeriesTitle
		}
	default:
		query.Type = KindMovie
		query.IMDbID = video.IMDbID
	}
	return query
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// Download queues one provider result for a catalog video.
func (s *Service) Download(ctx context.Context, kind, id string, request DownloadRequest) (Job, error) {
	video, err := s.resolveVideo(ctx, kind, id)
	if err != nil {
		return Job{}, err
	}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return Job{}, err
	}
	if language, ok := NormalizeLanguage(request.Language); ok {
		request.Language = language
	} else if request.Language != "" {
		return Job{}, fmt.Errorf("%w: unsupported language code %q", ErrInvalid, request.Language)
	}
	if request.FileID == "" || len(request.FileID) > 32 {
		return Job{}, fmt.Errorf("%w: a provider file ID is required", ErrInvalid)
	}
	var found bool
	for _, item := range cfg.Providers {
		if item.ID == request.ProviderID && item.Enabled {
			found = true
			break
		}
	}
	if !found {
		return Job{}, fmt.Errorf("%w: provider %q is not enabled", ErrInvalid, request.ProviderID)
	}
	return s.enqueueDownload(ctx, video, request, "")
}

func (s *Service) enqueueDownload(ctx context.Context, video Video, request DownloadRequest, detail string) (Job, error) {
	payload, err := json.Marshal(downloadPayload{Request: request})
	if err != nil {
		return Job{}, fmt.Errorf("%w: download request could not be encoded", ErrInvalid)
	}
	if detail == "" {
		detail = "queued " + request.Language + " download"
		if request.FileName != "" {
			detail = "queued " + truncate(request.FileName, 120)
		}
	}
	job, err := s.store.CreateJob(ctx, Job{
		Kind: "download", VideoKind: video.Kind, VideoID: video.ID, Language: request.Language, Detail: detail,
	}, payload)
	if err != nil {
		return Job{}, err
	}
	s.wakeup()
	return job, nil
}

// runDownload fetches one provider result, validates it, and installs the sidecar atomically.
func (s *Service) runDownload(ctx context.Context, job Job, background context.Context) (string, error) {
	var payload downloadPayload
	if err := s.store.JobPayload(ctx, job.ID, &payload); err != nil {
		return "", err
	}
	video, err := s.resolveVideo(ctx, job.VideoKind, job.VideoID)
	if err != nil {
		return "", err
	}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return "", err
	}
	var chosen provider
	for _, item := range s.configuredProviders(cfg) {
		if item.ID() == payload.Request.ProviderID {
			chosen = item
			break
		}
	}
	if chosen == nil {
		return "", fmt.Errorf("%w: provider %q is not configured", ErrNotConfigured, payload.Request.ProviderID)
	}
	// Bound the whole provider download: one API call plus the linked payload fetch.
	downloadCtx, cancelDownload := context.WithTimeout(ctx, time.Duration(clamp(cfg.ProviderTimeoutSeconds, 5, 120, defaultProviderSecs)*3)*time.Second)
	defer cancelDownload()
	downloaded, err := chosen.Download(downloadCtx, payload.Request.FileID)
	if err != nil {
		s.noteDownloadFailure(ctx, cfg, video, payload.Request, chosen, err)
		return "", err
	}
	s.clearProviderError(chosen.ID(), downloaded.Remaining, downloaded.ResetAt)
	sidecar, err := PublishSidecar(ctx, PublishTarget{
		RootPath: video.rootPath, VideoPath: video.Path,
		Language: payload.Request.Language, Forced: payload.Request.Forced, HI: payload.Request.HI,
		Format: downloaded.Format, Source: "provider:" + chosen.ID(), Data: downloaded.Data,
	})
	if err != nil {
		return "", err
	}
	sidecar.Kind, sidecar.VideoID = video.Kind, video.ID
	if err := s.store.UpsertSidecar(ctx, sidecar); err != nil {
		return "", err
	}
	// A successful install satisfies the wanted variant until the next scan confirms it.
	_ = s.store.SetWantedStatus(background, video.Kind, video.ID, payload.Request.Language,
		payload.Request.Forced, payload.Request.HI, "satisfied")
	detail := fmt.Sprintf("installed %s from %s", sidecar.Path, chosen.Name())
	if downloaded.FileName != "" {
		detail = fmt.Sprintf("%s (%s)", detail, truncate(downloaded.FileName, 120))
	}
	if downloaded.Remaining > 0 {
		detail = fmt.Sprintf("%s; %d provider downloads remain today", detail, downloaded.Remaining)
	}
	_ = s.store.Event(background, HistoryEntry{
		Kind: video.Kind, VideoID: video.ID, Action: "downloaded",
		Language: payload.Request.Language, Message: detail,
	})
	return detail, nil
}

func (s *Service) noteDownloadFailure(ctx context.Context, cfg Config, video Video, request DownloadRequest, chosen provider, err error) {
	message := publicError(err)
	_ = s.store.RecordFailure(ctx, video.Kind, video.ID, request.Language, chosen.ID(), message)
	_ = s.store.Event(ctx, HistoryEntry{
		Kind: video.Kind, VideoID: video.ID, Action: "download-failed",
		Language: request.Language, Message: message,
	})
	next := time.Now().Add(time.Duration(clamp(cfg.RetryMinutes, 5, 1440, defaultRetryMinutes)) * time.Minute)
	_ = s.store.MarkWantedAttempt(ctx, Wanted{
		Kind: video.Kind, VideoID: video.ID, Language: request.Language,
		Forced: request.Forced, HI: request.HI,
	}, "wanted", message, next)
	switch {
	case errors.Is(err, ErrQuota), errors.Is(err, ErrRateLimited):
		s.noteProviderError(chosen.ID(), message, 0, "")
	}
}

// autoSearchItem searches one wanted variant and queues the best result above the cutoff.
func (s *Service) autoSearchItem(ctx context.Context, cfg Config, item Wanted) {
	video, err := s.catalog.Video(ctx, item.Kind, item.VideoID)
	next := time.Now().Add(time.Duration(clamp(cfg.RetryMinutes, 5, 1440, defaultRetryMinutes)) * time.Minute)
	if err != nil {
		_ = s.store.MarkWantedAttempt(ctx, item, "wanted", "video file is not available", next)
		return
	}
	pref := LanguagePreference{Code: item.Language, Forced: item.Forced, HI: item.HI}
	outcome := s.searchVariants(ctx, cfg, video, []LanguagePreference{pref})
	if len(outcome.Results) == 0 {
		message := "no subtitles found"
		if len(outcome.Warnings) > 0 {
			message = outcome.Warnings[0]
		}
		_ = s.store.MarkWantedAttempt(ctx, item, "wanted", message, next)
		return
	}
	best := outcome.Results[0]
	if !cfg.AutoDownload || best.Score < cfg.CutoffScore {
		message := fmt.Sprintf("best result scored %d, below the cutoff of %d", best.Score, cfg.CutoffScore)
		_ = s.store.MarkWantedAttempt(ctx, item, "wanted", message, next)
		return
	}
	job, err := s.enqueueDownload(ctx, video, DownloadRequest{
		ProviderID: best.ProviderID, FileID: best.FileID, Language: item.Language,
		Forced: item.Forced, HI: item.HI, FileName: best.FileName,
	}, "")
	if err != nil {
		_ = s.store.MarkWantedAttempt(ctx, item, "wanted", publicError(err), next)
		return
	}
	retry := time.Now().Add(time.Duration(clamp(cfg.SearchIntervalHours, 1, 168, defaultSearchHours)) * time.Hour)
	_ = s.store.MarkWantedAttempt(ctx, item, "wanted", "", retry)
	_ = s.store.Event(ctx, HistoryEntry{
		Kind: video.Kind, VideoID: video.ID, Action: "queued",
		Language: item.Language, Message: fmt.Sprintf("queued %s (score %d, job %s)", best.FileName, best.Score, job.ID),
	})
}

// Sync queues an offset, frame-rate, audio, or reference synchronization job.
func (s *Service) Sync(ctx context.Context, kind, id string, request SyncRequest) (Job, error) {
	video, err := s.resolveVideo(ctx, kind, id)
	if err != nil {
		return Job{}, err
	}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return Job{}, err
	}
	request.Mode = strings.ToLower(strings.TrimSpace(request.Mode))
	request.Path, err = s.validateSidecarInput(video, request.Path)
	if err != nil {
		return Job{}, err
	}
	switch request.Mode {
	case "offset":
		if request.OffsetSeconds == 0 {
			return Job{}, fmt.Errorf("%w: enter a non-zero offset", ErrInvalid)
		}
		if request.OffsetSeconds < -600 || request.OffsetSeconds > 600 {
			return Job{}, fmt.Errorf("%w: offset must be between -600 and 600 seconds", ErrInvalid)
		}
	case "fps":
		if request.FPSFrom < 1 || request.FPSFrom > 240 || request.FPSTo < 1 || request.FPSTo > 240 {
			return Job{}, fmt.Errorf("%w: frame rates must be between 1 and 240", ErrInvalid)
		}
	case "audio":
		request.ReferencePath = ""
		if request.AudioStream != nil && (*request.AudioStream < -1 || *request.AudioStream > cfg.Sync.MaxEmbeddedStreamIndex) {
			return Job{}, fmt.Errorf("%w: audio stream index is out of range", ErrInvalid)
		}
	case "reference":
		request.ReferencePath, err = s.validateReference(video, request.ReferencePath)
		if err != nil {
			return Job{}, err
		}
	default:
		return Job{}, fmt.Errorf("%w: sync mode must be offset, fps, audio, or reference", ErrInvalid)
	}
	if request.MaxOffset < 0 || request.MaxOffset > 600 {
		return Job{}, fmt.Errorf("%w: max offset must be between 0 and 600 seconds", ErrInvalid)
	}
	request.VAD = strings.ToLower(strings.TrimSpace(request.VAD))
	if request.VAD != "" && !allowedVAD[request.VAD] {
		return Job{}, fmt.Errorf("%w: unsupported VAD %q", ErrInvalid, request.VAD)
	}
	payload, err := json.Marshal(syncPayload{Request: request})
	if err != nil {
		return Job{}, fmt.Errorf("%w: sync request could not be encoded", ErrInvalid)
	}
	job, err := s.store.CreateJob(ctx, Job{
		Kind: "sync", VideoKind: video.Kind, VideoID: video.ID,
		Detail: fmt.Sprintf("queued %s sync for %s", request.Mode, path.Base(request.Path)),
	}, payload)
	if err != nil {
		return Job{}, err
	}
	s.wakeup()
	return job, nil
}

// runSync executes one synchronization and publishes or stages the result.
func (s *Service) runSync(ctx context.Context, job Job) (string, error) {
	var payload syncPayload
	if err := s.store.JobPayload(ctx, job.ID, &payload); err != nil {
		return "", err
	}
	request := payload.Request
	video, err := s.resolveVideo(ctx, job.VideoKind, job.VideoID)
	if err != nil {
		return "", err
	}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return "", err
	}
	inputRel, err := s.validateSidecarInput(video, request.Path)
	if err != nil {
		return "", err
	}
	data, err := ReadSubtitle(video.rootPath, inputRel)
	if err != nil {
		return "", err
	}
	if _, err := ParseDocument(data); err != nil {
		return "", err
	}
	options := SyncOptions{
		Mode:            request.Mode,
		Input:           sidecarAbsPath(video, inputRel),
		Offset:          request.OffsetSeconds,
		FPSFrom:         request.FPSFrom,
		FPSTo:           request.FPSTo,
		MaxOffset:       firstNonZero(request.MaxOffset, cfg.Sync.MaxOffsetSeconds),
		MinScore:        request.MinScore,
		QualityMaxOff:   cfg.Sync.QualityMaxOffsetSecs,
		MaxFramerateDev: cfg.Sync.MaxFramerateDeviation,
		NoFixFramerate:  request.NoFixFramerate,
		GoldenSection:   request.GoldenSection,
		VAD:             firstNonEmpty(request.VAD, cfg.Sync.VAD),
		AudioStream:     audioStreamIndex(request.AudioStream),
		AudioSeconds:    cfg.Sync.AudioReferenceSeconds,
		TimeoutSeconds:  cfg.Sync.TimeoutSeconds,
		HelperPath:      cfg.Sync.HelperPath,
		FFmpegPath:      cfg.Sync.FFmpegPath,
		RootPath:        video.rootPath,
	}
	if request.Mode == "reference" {
		reference, err := s.validateReference(video, request.ReferencePath)
		if err != nil {
			return "", err
		}
		options.Reference = sidecarAbsPath(video, reference)
	}
	if request.Mode == "audio" {
		options.Reference = sidecarAbsPath(video, video.Path)
	}
	result, output, err := RunSync(ctx, options)
	if err != nil {
		return "", err
	}
	parsed, err := ParseDocument(output)
	if err != nil {
		return "", err
	}
	name, _ := ParseSidecarName(video.Path, path.Base(inputRel))
	if request.Preview {
		staged, err := s.store.SaveOutput(ctx, Output{
			JobID: job.ID, VideoKind: video.Kind, VideoID: video.ID,
			Language: name.Language, Forced: name.Forced, HI: name.HI, Format: string(parsed.Format),
			OriginPath: inputRel, TargetPath: inputRel, Payload: string(output),
			Detail: fmt.Sprintf("%s sync preview", request.Mode),
		})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s; review output %s", result.Detail, staged.ID), nil
	}
	sidecar, err := PublishSidecar(ctx, PublishTarget{
		RootPath: video.rootPath, VideoPath: video.Path, Language: name.Language,
		Forced: name.Forced, HI: name.HI, Format: parsed.Format, Source: "sync",
		Data: output, Path: inputRel,
	})
	if err != nil {
		return "", err
	}
	sidecar.Kind, sidecar.VideoID = video.Kind, video.ID
	if err := s.store.UpsertSidecar(ctx, sidecar); err != nil {
		return "", err
	}
	detail := fmt.Sprintf("synced %s: %s (offset %.3fs, scale %.4f)", sidecar.Path, result.Detail, result.Offset, result.Scale)
	_ = s.store.Event(ctx, HistoryEntry{
		Kind: video.Kind, VideoID: video.ID, Action: "synced",
		Language: name.Language, Message: detail,
	})
	return detail, nil
}

// Translate queues an AI translation that is staged for review before it is saved.
func (s *Service) Translate(ctx context.Context, kind, id string, request TranslateRequest) (Job, error) {
	video, err := s.resolveVideo(ctx, kind, id)
	if err != nil {
		return Job{}, err
	}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return Job{}, err
	}
	if _, err := s.translatorFor(cfg); err != nil {
		return Job{}, err
	}
	language, ok := NormalizeLanguage(request.Language)
	if !ok {
		return Job{}, fmt.Errorf("%w: choose a target language", ErrInvalid)
	}
	request.Language = language
	request.Path, err = s.validateSidecarInput(video, request.Path)
	if err != nil {
		return Job{}, err
	}
	if request.SourceLanguage != "" {
		if source, ok := NormalizeLanguage(request.SourceLanguage); ok {
			request.SourceLanguage = source
		} else {
			request.SourceLanguage = ""
		}
	}
	payload, err := json.Marshal(translatePayload{
		Path: request.Path, Language: request.Language, SourceLanguage: request.SourceLanguage,
	})
	if err != nil {
		return Job{}, fmt.Errorf("%w: translation request could not be encoded", ErrInvalid)
	}
	job, err := s.store.CreateJob(ctx, Job{
		Kind: "translate", VideoKind: video.Kind, VideoID: video.ID, Language: request.Language,
		Detail: fmt.Sprintf("queued %s translation of %s", request.Language, path.Base(request.Path)),
	}, payload)
	if err != nil {
		return Job{}, err
	}
	s.wakeup()
	return job, nil
}

func (s *Service) runTranslate(ctx context.Context, job Job, background context.Context) (string, error) {
	var payload translatePayload
	if err := s.store.JobPayload(ctx, job.ID, &payload); err != nil {
		return "", err
	}
	video, err := s.resolveVideo(ctx, job.VideoKind, job.VideoID)
	if err != nil {
		return "", err
	}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return "", err
	}
	translator, err := s.translatorFor(cfg)
	if err != nil {
		return "", err
	}
	inputRel, err := s.validateSidecarInput(video, payload.Path)
	if err != nil {
		return "", err
	}
	data, err := ReadSubtitle(video.rootPath, inputRel)
	if err != nil {
		return "", err
	}
	document, err := ParseDocument(data)
	if err != nil {
		return "", err
	}
	progress := func(done, total int) {
		percent := 5
		if total > 0 {
			percent = 5 + (done*90)/total
		}
		_ = s.store.UpdateJob(background, job.ID, "running", percent, fmt.Sprintf("translated %d of %d chunks", done, total), "")
	}
	output, result, err := TranslateDocument(ctx, translator, document, TranslateRequest{
		Language: payload.Language, SourceLanguage: payload.SourceLanguage,
	}, cfg.AI, progress)
	if err != nil {
		if errors.Is(err, ErrNotConfigured) {
			return "", fmt.Errorf("%w: set up the shared AI provider in Connections", ErrNoTranslator)
		}
		return "", err
	}
	name, _ := ParseSidecarName(video.Path, path.Base(inputRel))
	staged, err := s.store.SaveOutput(ctx, Output{
		JobID: job.ID, VideoKind: video.Kind, VideoID: video.ID,
		Language: payload.Language, Forced: name.Forced, HI: name.HI, Format: string(document.Format),
		OriginPath: inputRel, Payload: string(output),
		Detail: fmt.Sprintf("%s translation of %s", payload.Language, path.Base(inputRel)),
	})
	if err != nil {
		return "", err
	}
	detail := fmt.Sprintf("translated %d cues into %s with %d requests and %d tokens; review output %s",
		result.Cues, payload.Language, result.Requests, result.Tokens, staged.ID)
	_ = s.store.Event(ctx, HistoryEntry{
		Kind: video.Kind, VideoID: video.ID, Action: "translated",
		Language: payload.Language, Message: detail,
	})
	return detail, nil
}

// Extract queues extraction of an embedded subtitle stream.
func (s *Service) Extract(ctx context.Context, kind, id string, request ExtractRequest) (Job, error) {
	video, err := s.resolveVideo(ctx, kind, id)
	if err != nil {
		return Job{}, err
	}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return Job{}, err
	}
	if request.StreamIndex < 0 || request.StreamIndex > cfg.Sync.MaxEmbeddedStreamIndex {
		return Job{}, fmt.Errorf("%w: subtitle stream index is out of range", ErrInvalid)
	}
	if request.Language != "" {
		language, ok := NormalizeLanguage(request.Language)
		if !ok {
			return Job{}, fmt.Errorf("%w: unsupported language code %q", ErrInvalid, request.Language)
		}
		request.Language = language
	}
	payload, err := json.Marshal(extractPayload{
		StreamIndex: request.StreamIndex, Language: request.Language, Preview: request.Preview,
		Forced: request.Forced, HI: request.HI,
	})
	if err != nil {
		return Job{}, fmt.Errorf("%w: extraction request could not be encoded", ErrInvalid)
	}
	job, err := s.store.CreateJob(ctx, Job{
		Kind: "extract", VideoKind: video.Kind, VideoID: video.ID, Language: request.Language,
		Detail: fmt.Sprintf("queued extraction of subtitle stream %d", request.StreamIndex),
	}, payload)
	if err != nil {
		return Job{}, err
	}
	s.wakeup()
	return job, nil
}

func (s *Service) runExtract(ctx context.Context, job Job) (string, error) {
	var payload extractPayload
	if err := s.store.JobPayload(ctx, job.ID, &payload); err != nil {
		return "", err
	}
	video, err := s.resolveVideo(ctx, job.VideoKind, job.VideoID)
	if err != nil {
		return "", err
	}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return "", err
	}
	streams, err := ProbeStreams(ctx, cfg.Sync.FFmpegPath, sidecarAbsPath(video, video.Path))
	if err != nil {
		return "", err
	}
	var chosen Stream
	found := false
	for _, stream := range streams {
		if stream.Index == payload.StreamIndex && stream.Type == "subtitle" {
			chosen, found = stream, true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("%w: subtitle stream %d does not exist", ErrNotFound, payload.StreamIndex)
	}
	language := payload.Language
	if language == "" {
		language = chosen.Language
	}
	if language == "" {
		return "", fmt.Errorf("%w: the embedded stream has no language tag; choose one", ErrInvalid)
	}
	normalized, ok := NormalizeLanguage(language)
	if !ok {
		return "", fmt.Errorf("%w: unsupported language code %q", ErrInvalid, language)
	}
	// Stream disposition and title hints define the variant unless the request overrides them.
	forced, hi := chosen.Forced, chosen.HI
	if payload.Forced != nil {
		forced = *payload.Forced
	}
	if payload.HI != nil {
		hi = *payload.HI
	}
	variant := variantDetail(normalized, forced, hi)
	work, err := os.CreateTemp("", "constellarr-extract-*.srt")
	if err != nil {
		return "", errors.New("subtitles: a temporary file could not be created")
	}
	outputPath := work.Name()
	work.Close()
	defer os.Remove(outputPath)
	if err := ExtractEmbeddedSubtitle(ctx, cfg.Sync.FFmpegPath, sidecarAbsPath(video, video.Path), payload.StreamIndex, outputPath); err != nil {
		return "", err
	}
	data, err := readBoundedFile(ctx, outputPath)
	if err != nil {
		return "", err
	}
	document, err := ParseDocument(data)
	if err != nil {
		return "", err
	}
	if payload.Preview {
		staged, err := s.store.SaveOutput(ctx, Output{
			JobID: job.ID, VideoKind: video.Kind, VideoID: video.ID, Language: normalized,
			Forced: forced, HI: hi, Format: string(document.Format), Payload: string(data),
			Detail: fmt.Sprintf("embedded stream %d (%s)", payload.StreamIndex, variant),
		})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("extracted stream %d (%s); review output %s", payload.StreamIndex, variant, staged.ID), nil
	}
	sidecar, err := PublishSidecar(ctx, PublishTarget{
		RootPath: video.rootPath, VideoPath: video.Path, Language: normalized,
		Forced: forced, HI: hi, Format: document.Format, Source: "embedded", Data: data,
	})
	if err != nil {
		return "", err
	}
	sidecar.Kind, sidecar.VideoID = video.Kind, video.ID
	if err := s.store.UpsertSidecar(ctx, sidecar); err != nil {
		return "", err
	}
	detail := fmt.Sprintf("extracted stream %d (%s) to %s", payload.StreamIndex, variant, sidecar.Path)
	_ = s.store.Event(ctx, HistoryEntry{
		Kind: video.Kind, VideoID: video.ID, Action: "extracted", Language: normalized, Message: detail,
	})
	return detail, nil
}

// Streams lists embedded audio and subtitle streams of one video.
func (s *Service) Streams(ctx context.Context, kind, id string) ([]Stream, error) {
	video, err := s.resolveVideo(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return nil, err
	}
	return ProbeStreams(ctx, cfg.Sync.FFmpegPath, sidecarAbsPath(video, video.Path))
}

func (s *Service) Output(ctx context.Context, id string) (Output, error) {
	if strings.TrimSpace(id) == "" || len(id) > 128 {
		return Output{}, fmt.Errorf("%w: output id is invalid", ErrInvalid)
	}
	return s.store.Output(ctx, id)
}

// ApplyOutput installs a reviewed output as its sidecar, recycling any previous file.
func (s *Service) ApplyOutput(ctx context.Context, id string) (Sidecar, error) {
	output, err := s.store.Output(ctx, id)
	if err != nil {
		return Sidecar{}, err
	}
	if output.Status != "pending" {
		return Sidecar{}, fmt.Errorf("%w: this output was already reviewed", ErrConflict)
	}
	video, err := s.resolveVideo(ctx, output.VideoKind, output.VideoID)
	if err != nil {
		return Sidecar{}, err
	}
	data := []byte(output.Payload)
	document, err := ParseDocument(data)
	if err != nil {
		return Sidecar{}, fmt.Errorf("%w: stored output is no longer valid", ErrInvalid)
	}
	target := PublishTarget{
		RootPath: video.rootPath, VideoPath: video.Path,
		Language: output.Language, Forced: output.Forced, HI: output.HI,
		Format: document.Format, Source: "review", Data: data,
	}
	if output.TargetPath != "" {
		if _, err := cleanRel(output.TargetPath); err != nil {
			return Sidecar{}, err
		}
		target.Path = output.TargetPath
	}
	sidecar, err := PublishSidecar(ctx, target)
	if err != nil {
		return Sidecar{}, err
	}
	if output.TargetPath != "" {
		_ = s.store.DeleteSidecar(ctx, video.Kind, video.ID, output.TargetPath)
	}
	sidecar.Kind, sidecar.VideoID = video.Kind, video.ID
	if err := s.store.UpsertSidecar(ctx, sidecar); err != nil {
		return Sidecar{}, err
	}
	if err := s.store.MarkOutputApplied(ctx, output.ID); err != nil {
		return Sidecar{}, err
	}
	_ = s.store.Event(ctx, HistoryEntry{
		Kind: video.Kind, VideoID: video.ID, Action: "applied",
		Language: output.Language, Message: "saved " + sidecar.Path,
	})
	return sidecar, nil
}

// DiscardOutput deletes a pending output without touching the subtitles on disk.
func (s *Service) DiscardOutput(ctx context.Context, id string) error {
	output, err := s.store.Output(ctx, id)
	if err != nil {
		return err
	}
	if output.Status != "pending" {
		return fmt.Errorf("%w: this output was already reviewed", ErrConflict)
	}
	if err := s.store.DeleteOutput(ctx, id); err != nil {
		return err
	}
	_ = s.store.Event(ctx, HistoryEntry{
		Kind: output.VideoKind, VideoID: output.VideoID, Action: "discarded",
		Language: output.Language, Message: "discarded staged subtitle",
	})
	return nil
}

// validateSidecarInput ensures the subtitle belongs to the video and exists on disk.
func (s *Service) validateSidecarInput(video Video, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("%w: a subtitle file is required", ErrInvalid)
	}
	clean, err := ResolveRelativePath(video.rootPath, rel)
	if err != nil {
		return "", err
	}
	if err := ValidateSidecarPath(video.Path, clean); err != nil {
		return "", err
	}
	handle, err := library.Open(video.rootPath, clean)
	if err != nil {
		return "", fmt.Errorf("%w: subtitle file is not available", ErrNotFound)
	}
	info, statErr := handle.Stat()
	handle.Close()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("%w: subtitle file is not usable", ErrInvalid)
	}
	return clean, nil
}

// validateReference accepts any subtitle file below the video's library root.
func (s *Service) validateReference(video Video, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("%w: choose a reference subtitle or audio track", ErrInvalid)
	}
	clean, err := ResolveRelativePath(video.rootPath, rel)
	if err != nil {
		return "", err
	}
	if _, ok := sidecarFormats[strings.ToLower(path.Ext(clean))]; !ok {
		return "", fmt.Errorf("%w: the reference must be a subtitle file", ErrInvalid)
	}
	handle, err := library.Open(video.rootPath, clean)
	if err != nil {
		return "", fmt.Errorf("%w: reference subtitle is not available", ErrNotFound)
	}
	info, statErr := handle.Stat()
	handle.Close()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("%w: reference subtitle is not usable", ErrInvalid)
	}
	return clean, nil
}

// translatorFor returns the injected translator or builds the configured OpenAI-compatible client.
func (s *Service) translatorFor(cfg Config) (Translator, error) {
	s.translatorMu.RLock()
	translator := s.translator
	s.translatorMu.RUnlock()
	if translator != nil {
		return translator, nil
	}
	if !cfg.AI.Enabled {
		return nil, fmt.Errorf("%w: set up the shared AI provider in Connections", ErrNoTranslator)
	}
	return NewOpenAITranslator(cfg.AI)
}

// audioStreamIndex maps an omitted or negative selection to automatic.
func audioStreamIndex(stream *int) int {
	if stream == nil || *stream < 0 {
		return -1
	}
	return *stream
}

func firstNonZero(primary, fallback float64) float64 {
	if primary != 0 {
		return primary
	}
	return fallback
}

// variantDetail renders a language variant for job and history messages.
func variantDetail(language string, forced, hi bool) string {
	detail := language
	if forced {
		detail += " forced"
	}
	if hi {
		detail += " HI"
	}
	return detail
}

// sidecarAbsPath joins a validated relative path to its library root for ffmpeg and ffsubsync.
func sidecarAbsPath(video Video, rel string) string {
	return filepath.Join(video.rootPath, filepath.FromSlash(rel))
}
