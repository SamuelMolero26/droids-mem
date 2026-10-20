package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

const contextBudgetReason = "encoded_response_budget"

type contextDestination uint8

const (
	contextLastSession contextDestination = iota
	contextUserRules
	contextBrowse
)

type contextBodyMode uint8

const (
	contextBodyNone contextBodyMode = iota
	contextBodyLearned
	contextBodySnippet
	contextBodyFull
)

type contextCandidate struct {
	memory      ContextMemory
	destination contextDestination
	bodyMode    contextBodyMode
}

type selectedContextCandidate struct {
	candidate contextCandidate
	index     int
}

func buildContextResponseConn(
	ctx context.Context,
	conn *sql.Conn,
	taskType string,
	ftsQuery string,
	mode ContextMode,
) (*ContextResponse, error) {
	resp := &ContextResponse{
		TaskType:  taskType,
		UserRules: []ContextMemory{},
		Browse:    []ContextMemory{},
		// Account for the field while selecting. It is removed before return
		// when the budget did not force any downgrade or omission.
		Reason: contextBudgetReason,
	}

	candidates, expected, rulesTotal, err := fetchContextCandidatesConn(
		ctx,
		conn,
		taskType,
		ftsQuery,
		mode,
	)
	if err != nil {
		return nil, err
	}
	resp.UserRulesTotal = rulesTotal

	selected := make([]selectedContextCandidate, 0, len(candidates))
	selectedByKind := map[string]int{}
	for _, candidate := range candidates {
		index := appendContextCandidate(resp, candidate)
		resp.StubbedCount++
		fits, err := contextFits(resp, contextSelectionBytes)
		if err != nil {
			return nil, err
		}
		if !fits {
			removeContextCandidate(resp, candidate.destination)
			resp.StubbedCount--
			break
		}
		selected = append(selected, selectedContextCandidate{
			candidate: candidate,
			index:     index,
		})
		selectedByKind[candidate.memory.Kind]++
	}

	setContextOmissions(resp, expected, selectedByKind)
	budgetConstrained := resp.OmittedCount > 0
	for i := range selected {
		candidate := &selected[i]
		if candidate.candidate.bodyMode == contextBodyNone {
			continue
		}

		what, learned, err := fetchContextBodyConn(ctx, conn, candidate.candidate.memory.ID)
		if err != nil {
			return nil, err
		}
		memory := selectedContextMemory(resp, *candidate)
		old := *memory
		oldStubbed := resp.StubbedCount
		switch candidate.candidate.bodyMode {
		case contextBodyLearned:
			memory.Learned = learned
			resp.StubbedCount--
		case contextBodySnippet:
			memory.Snippet = snippet(what, browseSnippetChars)
		case contextBodyFull:
			memory.What = what
			memory.Learned = learned
			resp.StubbedCount--
		}

		fits, err := contextFits(resp, MaxContextPayloadBytes)
		if err != nil {
			return nil, err
		}
		if !fits {
			*memory = old
			resp.StubbedCount = oldStubbed
			budgetConstrained = true
			break
		}
	}

	if !budgetConstrained {
		resp.Reason = ""
	}
	if fits, err := contextFits(resp, MaxContextPayloadBytes); err != nil {
		return nil, err
	} else if !fits {
		return nil, fmt.Errorf("context response exceeds internal %d-byte budget", MaxContextPayloadBytes)
	}
	return resp, nil
}

func fetchContextCandidatesConn(
	ctx context.Context,
	conn *sql.Conn,
	taskType string,
	ftsQuery string,
	mode ContextMode,
) ([]contextCandidate, map[string]int, int, error) {
	candidates := make([]contextCandidate, 0, contextRuleCandidateCap+browseErrorLimit+browseTaskLimit+1)
	expected := map[string]int{}

	last, err := fetchLastSessionMetadataConn(ctx, conn, taskType)
	if err != nil {
		return nil, nil, 0, err
	}
	if last != nil {
		candidates = append(candidates, contextCandidate{
			memory:      *last,
			destination: contextLastSession,
			bodyMode:    contextBodyLearned,
		})
		expected["session_summary"] = 1
	}

	rulesTotal, err := countUserRulesConn(ctx, conn, taskType)
	if err != nil {
		return nil, nil, 0, err
	}
	ruleLimit := contextRuleCandidateCap
	if mode == ModeRefresh {
		ruleLimit = maxAlwaysTierUserRules
		expected["user_rule"] = min(rulesTotal, maxAlwaysTierUserRules)
	} else {
		expected["user_rule"] = rulesTotal
	}
	rules, err := fetchUserRuleMetadataConn(ctx, conn, taskType, ruleLimit)
	if err != nil {
		return nil, nil, 0, err
	}
	for i, rule := range rules {
		destination := contextUserRules
		bodyMode := contextBodyLearned
		if mode == ModeOrient && i >= maxAlwaysTierUserRules {
			destination = contextBrowse
			bodyMode = contextBodyNone
			rule.Tier = "browse"
		}
		candidates = append(candidates, contextCandidate{
			memory:      rule,
			destination: destination,
			bodyMode:    bodyMode,
		})
	}

	if mode == ModeRefresh {
		return candidates, expected, rulesTotal, nil
	}

	errorLimit, taskLimit := browseErrorLimit, browseTaskLimit
	bodyMode := contextBodySnippet
	if mode == ModeDeep {
		errorLimit, taskLimit = deepErrorLimit, deepTaskLimit
		bodyMode = contextBodyFull
	}
	errorsMetadata, err := fetchBrowseMetadataConn(ctx, conn, ftsQuery, taskType, "error_resolution", errorLimit)
	if err != nil {
		return nil, nil, 0, err
	}
	patternsMetadata, err := fetchBrowseMetadataConn(ctx, conn, ftsQuery, taskType, "task_pattern", taskLimit)
	if err != nil {
		return nil, nil, 0, err
	}
	expected["error_resolution"] = len(errorsMetadata)
	expected["task_pattern"] = len(patternsMetadata)
	for _, memory := range append(errorsMetadata, patternsMetadata...) {
		candidates = append(candidates, contextCandidate{
			memory:      memory,
			destination: contextBrowse,
			bodyMode:    bodyMode,
		})
	}
	return candidates, expected, rulesTotal, nil
}

func appendContextCandidate(resp *ContextResponse, candidate contextCandidate) int {
	switch candidate.destination {
	case contextLastSession:
		resp.LastSession = &candidate.memory
		return 0
	case contextUserRules:
		resp.UserRules = append(resp.UserRules, candidate.memory)
		return len(resp.UserRules) - 1
	default:
		resp.Browse = append(resp.Browse, candidate.memory)
		return len(resp.Browse) - 1
	}
}

func removeContextCandidate(resp *ContextResponse, destination contextDestination) {
	switch destination {
	case contextLastSession:
		resp.LastSession = nil
	case contextUserRules:
		resp.UserRules = resp.UserRules[:len(resp.UserRules)-1]
	default:
		resp.Browse = resp.Browse[:len(resp.Browse)-1]
	}
}

func selectedContextMemory(resp *ContextResponse, selected selectedContextCandidate) *ContextMemory {
	switch selected.candidate.destination {
	case contextLastSession:
		return resp.LastSession
	case contextUserRules:
		return &resp.UserRules[selected.index]
	default:
		return &resp.Browse[selected.index]
	}
}

func setContextOmissions(resp *ContextResponse, expected, selected map[string]int) {
	for _, kind := range []string{"session_summary", "user_rule", "error_resolution", "task_pattern"} {
		omitted := expected[kind] - selected[kind]
		if omitted <= 0 {
			continue
		}
		if resp.OmittedByKind == nil {
			resp.OmittedByKind = map[string]int{}
		}
		resp.OmittedByKind[kind] = omitted
		resp.OmittedCount += omitted
	}
}

func contextFits(resp *ContextResponse, maxBytes int) (bool, error) {
	encoded, err := json.Marshal(resp)
	if err != nil {
		return false, fmt.Errorf("marshal context response: %w", err)
	}
	return len(encoded) <= maxBytes, nil
}

func fetchLastSessionMetadataConn(ctx context.Context, conn *sql.Conn, taskType string) (*ContextMemory, error) {
	var memory ContextMemory
	err := conn.QueryRowContext(ctx, `
		SELECT id, kind, title, created_at, authored_at,
		       expand_count, COALESCE(last_expanded_at, 0)
		FROM memories
		WHERE task_type = ? AND kind = 'session_summary'
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, taskType).Scan(
		&memory.ID,
		&memory.Kind,
		&memory.Title,
		&memory.CreatedAt,
		&memory.AuthoredAt,
		&memory.ExpandCount,
		&memory.LastExpandedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fetch last session metadata: %w", err)
	}
	memory.Tier = "always"
	return &memory, nil
}

func countUserRulesConn(ctx context.Context, conn *sql.Conn, taskType string) (int, error) {
	var total int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM memories
		WHERE task_type = ? AND kind = 'user_rule'
	`, taskType).Scan(&total); err != nil {
		return 0, fmt.Errorf("count user rules: %w", err)
	}
	return total, nil
}

func fetchUserRuleMetadataConn(ctx context.Context, conn *sql.Conn, taskType string, limit int) ([]ContextMemory, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT id, kind, title, created_at, authored_at,
		       expand_count, COALESCE(last_expanded_at, 0)
		FROM memories
		WHERE task_type = ? AND kind = 'user_rule'
		ORDER BY created_at DESC, id DESC
		LIMIT ?
	`, taskType, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch user rule metadata: %w", err)
	}
	defer rows.Close()
	out := []ContextMemory{}
	for rows.Next() {
		var memory ContextMemory
		if err := rows.Scan(
			&memory.ID,
			&memory.Kind,
			&memory.Title,
			&memory.CreatedAt,
			&memory.AuthoredAt,
			&memory.ExpandCount,
			&memory.LastExpandedAt,
		); err != nil {
			return nil, fmt.Errorf("scan user rule metadata: %w", err)
		}
		memory.Tier = "always"
		out = append(out, memory)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user rule metadata: %w", err)
	}
	return out, nil
}

func fetchBrowseMetadataConn(
	ctx context.Context,
	conn *sql.Conn,
	ftsQuery string,
	taskType string,
	kind string,
	limit int,
) ([]ContextMemory, error) {
	const columns = `m.id, m.kind, m.title, m.created_at, m.authored_at,
		m.expand_count, COALESCE(m.last_expanded_at, 0)`
	var (
		rows *sql.Rows
		err  error
	)
	if ftsQuery == "" {
		rows, err = conn.QueryContext(ctx, `
			SELECT `+columns+`
			FROM memories m
			WHERE m.task_type = ? AND m.kind = ?
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT ?
		`, taskType, kind, limit)
	} else {
		rows, err = conn.QueryContext(ctx, `
			SELECT `+columns+`
			FROM memories_fts fts
			JOIN memories m ON m.rowid = fts.rowid
			WHERE memories_fts MATCH ?
			AND m.task_type = ? AND m.kind = ?
			ORDER BY bm25(memories_fts, 3, 1, 2, 1), m.created_at DESC, m.id DESC
			LIMIT ?
		`, ftsQuery, taskType, kind, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("fetch browse metadata (%s): %w", kind, err)
	}
	defer rows.Close()
	out := []ContextMemory{}
	for rows.Next() {
		var memory ContextMemory
		if err := rows.Scan(
			&memory.ID,
			&memory.Kind,
			&memory.Title,
			&memory.CreatedAt,
			&memory.AuthoredAt,
			&memory.ExpandCount,
			&memory.LastExpandedAt,
		); err != nil {
			return nil, fmt.Errorf("scan browse metadata (%s): %w", kind, err)
		}
		memory.Tier = "browse"
		out = append(out, memory)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate browse metadata (%s): %w", kind, err)
	}
	return out, nil
}

func fetchContextBodyConn(ctx context.Context, conn *sql.Conn, id string) (what, learned string, err error) {
	err = conn.QueryRowContext(ctx, `
		SELECT what, learned
		FROM memories
		WHERE id = ?
	`, id).Scan(&what, &learned)
	if err != nil {
		return "", "", fmt.Errorf("fetch context body %s: %w", id, err)
	}
	return what, learned, nil
}
