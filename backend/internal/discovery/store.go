package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// querier is satisfied by both the pool and a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const requestColumns = `id, user_id, media_type, provider, provider_id, title, year, poster, status,
	message, decision_note, decided_by, decided_at, library_id, delivery, created_at, updated_at`

const recommendationColumns = `id, user_id, model, media_type, input, candidates, warnings,
	accepted_at, accepted_action, accepted_id, created_at`

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func scanRequest(row pgx.Row) (Request, error) {
	var request Request
	var delivery []byte
	if err := row.Scan(&request.ID, &request.UserID, &request.MediaType, &request.Provider, &request.ProviderID,
		&request.Title, &request.Year, &request.Poster, &request.Status, &request.Message, &request.DecisionNote,
		&request.DecidedBy, &request.DecidedAt, &request.LibraryID, &delivery, &request.CreatedAt, &request.UpdatedAt); err != nil {
		return Request{}, err
	}
	request.Delivery = decodeDelivery(delivery)
	return request, nil
}

func scanRequests(rows pgx.Rows) ([]Request, error) {
	requests := []Request{}
	for rows.Next() {
		request, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func decodeDelivery(raw []byte) Delivery {
	delivery := Delivery{}
	if len(raw) == 0 {
		return delivery
	}
	if err := json.Unmarshal(raw, &delivery); err != nil {
		return Delivery{Phase: PhaseUnknown}
	}
	return delivery
}

func encodeJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("discovery: request state could not be encoded")
	}
	return raw, nil
}

func (s *Service) requestByID(ctx context.Context, q querier, id string) (Request, error) {
	request, err := scanRequest(q.QueryRow(ctx, `SELECT `+requestColumns+` FROM discovery_requests WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, errors.New("discovery: requests could not be loaded")
	}
	return request, nil
}

// lockRequest reads one request under a row lock so status transitions cannot race.
func (s *Service) lockRequest(ctx context.Context, q querier, id string) (Request, error) {
	request, err := scanRequest(q.QueryRow(ctx, `SELECT `+requestColumns+` FROM discovery_requests WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, errors.New("discovery: requests could not be loaded")
	}
	return request, nil
}

func (s *Service) activeRequest(ctx context.Context, q querier, userID, mediaType, providerID string) (Request, error) {
	request, err := scanRequest(q.QueryRow(ctx, `SELECT `+requestColumns+` FROM discovery_requests
		WHERE user_id = $1 AND media_type = $2 AND provider_id = $3
		  AND status IN ('pending', 'approving', 'approved', 'available') LIMIT 1`, userID, mediaType, providerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, errors.New("discovery: requests could not be loaded")
	}
	return request, nil
}

func (s *Service) lastDecidedRequest(ctx context.Context, id, userID, mediaType, providerID string) (string, error) {
	var status string
	err := s.pool.QueryRow(ctx, `SELECT status FROM discovery_requests
		WHERE user_id = $2 AND media_type = $3 AND provider_id = $4 AND id <> $1
		ORDER BY created_at DESC LIMIT 1`,
		id, userID, mediaType, providerID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("discovery: requests could not be loaded")
	}
	return status, nil
}

func (s *Service) requestList(ctx context.Context, filter ListFilter, actor string, canApprove bool) ([]Request, error) {
	sql := `SELECT ` + requestColumns + ` FROM discovery_requests WHERE true`
	args := []any{}
	add := func(column string, value any) {
		args = append(args, value)
		sql += " AND " + column + " = $" + strconv.Itoa(len(args))
	}
	scope := actor
	if canApprove {
		scope = filter.UserID
	}
	if scope != "" {
		add("user_id", scope)
	}
	if filter.Status != "" {
		add("status", filter.Status)
	}
	if filter.MediaType != "" {
		add("media_type", filter.MediaType)
	}
	if filter.Query != "" {
		args = append(args, "%"+escapeLike(filter.Query)+"%")
		sql += " AND title ILIKE $" + strconv.Itoa(len(args))
	}
	sql += " ORDER BY created_at DESC LIMIT " + strconv.Itoa(filter.Limit)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, errors.New("discovery: requests could not be loaded")
	}
	defer rows.Close()
	return scanRequests(rows)
}

func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func (s *Service) insertRequest(ctx context.Context, q querier, request Request) error {
	delivery, err := encodeJSON(request.Delivery)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO discovery_requests
		(id, user_id, media_type, provider, provider_id, title, year, poster, status, message, library_id, delivery)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		request.ID, request.UserID, request.MediaType, request.Provider, request.ProviderID, request.Title,
		request.Year, request.Poster, request.Status, request.Message, request.LibraryID, delivery)
	return err
}

func (s *Service) setApproving(ctx context.Context, q querier, id, actor, note string, delivery Delivery) error {
	raw, err := encodeJSON(delivery)
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `UPDATE discovery_requests
		SET status = 'approving', decided_by = $2, decided_at = now(), decision_note = $3, delivery = $4, updated_at = now()
		WHERE id = $1`, id, actor, note, raw); err != nil {
		return errors.New("discovery: the approval could not be recorded")
	}
	return nil
}

func (s *Service) setDecision(ctx context.Context, q querier, id, status, actor, note, libraryID string, delivery Delivery) error {
	raw, err := encodeJSON(delivery)
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `UPDATE discovery_requests
		SET status = $2, decided_by = $3, decided_at = now(), decision_note = $4, library_id = $5, delivery = $6, updated_at = now()
		WHERE id = $1`, id, status, actor, note, libraryID, raw); err != nil {
		return errors.New("discovery: the decision could not be recorded")
	}
	return nil
}

func (s *Service) setDelivery(ctx context.Context, q querier, id string, delivery Delivery) error {
	raw, err := encodeJSON(delivery)
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `UPDATE discovery_requests SET delivery = $2, updated_at = now() WHERE id = $1`, id, raw); err != nil {
		return errors.New("discovery: request state could not be saved")
	}
	return nil
}

func (s *Service) setRejected(ctx context.Context, q querier, id, actor, reason string) error {
	if _, err := q.Exec(ctx, `UPDATE discovery_requests
		SET status = 'rejected', decided_by = $2, decided_at = now(), decision_note = $3, delivery = '{}'::jsonb, updated_at = now()
		WHERE id = $1`, id, actor, reason); err != nil {
		return errors.New("discovery: the decision could not be recorded")
	}
	return nil
}

func (s *Service) setCancelled(ctx context.Context, q querier, id string) error {
	if _, err := q.Exec(ctx, `UPDATE discovery_requests
		SET status = 'cancelled', delivery = '{}'::jsonb, updated_at = now() WHERE id = $1`, id); err != nil {
		return errors.New("discovery: the cancellation could not be recorded")
	}
	return nil
}

func (s *Service) setStatus(ctx context.Context, q querier, id, status string, delivery Delivery) error {
	raw, err := encodeJSON(delivery)
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `UPDATE discovery_requests SET status = $2, delivery = $3, updated_at = now() WHERE id = $1`,
		id, status, raw); err != nil {
		return errors.New("discovery: request state could not be saved")
	}
	return nil
}

func (s *Service) addEvent(ctx context.Context, q querier, requestID, actor, action, fromStatus, toStatus, message string) error {
	_, err := q.Exec(ctx, `INSERT INTO discovery_request_events (request_id, actor, action, from_status, to_status, message)
		VALUES ($1, $2, $3, $4, $5, $6)`, requestID, actor, action, fromStatus, toStatus, message)
	if err != nil {
		return errors.New("discovery: the audit trail could not be recorded")
	}
	return nil
}

func (s *Service) events(ctx context.Context, requestID string) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, request_id, actor, action, from_status, to_status, message, created_at
		FROM discovery_request_events WHERE request_id = $1 ORDER BY id LIMIT 200`, requestID)
	if err != nil {
		return nil, errors.New("discovery: the audit trail could not be loaded")
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.RequestID, &event.Actor, &event.Action, &event.FromStatus,
			&event.ToStatus, &event.Message, &event.CreatedAt); err != nil {
			return nil, errors.New("discovery: the audit trail could not be loaded")
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Service) addComment(ctx context.Context, requestID, userID, body string) (Comment, error) {
	comment := Comment{RequestID: requestID, UserID: userID, Body: body}
	if err := s.pool.QueryRow(ctx, `INSERT INTO discovery_request_comments (request_id, user_id, body)
		VALUES ($1, $2, $3) RETURNING id, created_at`, requestID, userID, body).Scan(&comment.ID, &comment.CreatedAt); err != nil {
		return Comment{}, errors.New("discovery: the comment could not be saved")
	}
	return comment, nil
}

func (s *Service) comments(ctx context.Context, requestID string) ([]Comment, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, request_id, user_id, body, created_at
		FROM discovery_request_comments WHERE request_id = $1 ORDER BY id LIMIT 200`, requestID)
	if err != nil {
		return nil, errors.New("discovery: comments could not be loaded")
	}
	defer rows.Close()
	comments := []Comment{}
	for rows.Next() {
		var comment Comment
		if err := rows.Scan(&comment.ID, &comment.RequestID, &comment.UserID, &comment.Body, &comment.CreatedAt); err != nil {
			return nil, errors.New("discovery: comments could not be loaded")
		}
		comments = append(comments, comment)
	}
	return comments, rows.Err()
}

// approvalsToResume lists interrupted approvals; updated_at keeps the retry from racing the original attempt.
func (s *Service) approvalsToResume(ctx context.Context, olderThan int, limit int) ([]Request, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+requestColumns+` FROM discovery_requests
		WHERE status = 'approving' AND updated_at < now() - make_interval(secs => $1)
		ORDER BY updated_at LIMIT $2`, olderThan, limit)
	if err != nil {
		return nil, errors.New("discovery: requests could not be loaded")
	}
	defer rows.Close()
	return scanRequests(rows)
}

func (s *Service) trackedRequests(ctx context.Context, limit int) ([]Request, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+requestColumns+` FROM discovery_requests
		WHERE status IN ('approved', 'available') ORDER BY updated_at LIMIT $1`, limit)
	if err != nil {
		return nil, errors.New("discovery: requests could not be loaded")
	}
	defer rows.Close()
	return scanRequests(rows)
}

func (s *Service) taskDue(ctx context.Context, name string, interval string) (bool, error) {
	var due bool
	err := s.pool.QueryRow(ctx, `SELECT NOT EXISTS (
		SELECT 1 FROM discovery_automation WHERE name = $1 AND last_run > now() - $2::interval)`, name, interval).Scan(&due)
	if err != nil {
		return false, errors.New("discovery: task state could not be loaded")
	}
	return due, nil
}

func (s *Service) markTask(ctx context.Context, name string) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO discovery_automation (name) VALUES ($1) ON CONFLICT (name) DO UPDATE SET last_run = now()`, name); err != nil {
		return errors.New("discovery: task state could not be saved")
	}
	return nil
}

func (s *Service) insertRecommendation(ctx context.Context, rec Recommendation) error {
	input, err := encodeJSON(rec.Input)
	if err != nil {
		return err
	}
	candidates, err := encodeJSON(rec.Candidates)
	if err != nil {
		return err
	}
	warnings, err := encodeJSON(rec.Warnings)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO discovery_recommendations
		(id, user_id, model, media_type, input, candidates, warnings) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		rec.ID, rec.UserID, rec.Model, rec.MediaType, input, candidates, warnings)
	if err != nil {
		return errors.New("discovery: the recommendation could not be saved")
	}
	return nil
}

func scanRecommendation(row pgx.Row) (Recommendation, error) {
	var rec Recommendation
	var input, candidates, warnings []byte
	if err := row.Scan(&rec.ID, &rec.UserID, &rec.Model, &rec.MediaType, &input, &candidates, &warnings,
		&rec.AcceptedAt, &rec.AcceptedAction, &rec.AcceptedID, &rec.CreatedAt); err != nil {
		return Recommendation{}, err
	}
	if err := json.Unmarshal(input, &rec.Input); err != nil {
		return Recommendation{}, errors.New("discovery: the recommendation is unreadable")
	}
	rec.Candidates = []Candidate{}
	if err := json.Unmarshal(candidates, &rec.Candidates); err != nil {
		return Recommendation{}, errors.New("discovery: the recommendation is unreadable")
	}
	rec.Warnings = []string{}
	_ = json.Unmarshal(warnings, &rec.Warnings)
	return rec, nil
}

func (s *Service) recommendation(ctx context.Context, id string) (Recommendation, error) {
	rec, err := scanRecommendation(s.pool.QueryRow(ctx, `SELECT `+recommendationColumns+` FROM discovery_recommendations WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Recommendation{}, ErrNotFound
	}
	if err != nil {
		return Recommendation{}, err
	}
	return rec, nil
}

func (s *Service) recommendations(ctx context.Context, userID string, limit int) ([]Recommendation, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+recommendationColumns+` FROM discovery_recommendations
		WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, errors.New("discovery: recommendations could not be loaded")
	}
	defer rows.Close()
	list := []Recommendation{}
	for rows.Next() {
		rec, err := scanRecommendation(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, rec)
	}
	return list, rows.Err()
}

func (s *Service) markRecommendationAccepted(ctx context.Context, rec Recommendation, action, id string) error {
	candidates, err := encodeJSON(rec.Candidates)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE discovery_recommendations
		SET accepted_at = now(), accepted_action = $2, accepted_id = $3, candidates = $4 WHERE id = $1`,
		rec.ID, action, id, candidates); err != nil {
		return errors.New("discovery: the recommendation could not be updated")
	}
	return nil
}
