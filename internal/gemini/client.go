// Package gemini is the Go-side Gemini API client used by the
// `stash serve` identify worker. It's a deliberate port of the
// Swift implementation in stash-mac (GeminiClient.swift,
// AIProvider.swift, AIResponseParser.swift) so identify behaves
// the same regardless of which device runs it — same wire
// format, same response parsing, same prompt.
//
// The daemon reads the API key from the system keychain on every
// call (via internal/credentials) rather than caching in-process.
// This way `stash auth refresh-gemini` takes effect immediately,
// no daemon restart needed.
package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/msjurset/gostash/internal/config"
)

// DefaultModel matches the Mac client's default. Override per call
// via Client.Model if a fallback chain needs to route some calls
// to Pro etc.
const (
	DefaultModel   = "gemini-2.5-flash"
	EmbeddingModel = "gemini-embedding-001"
)

var backoffMin = 1 * time.Second


// Media is the input shape for identify — bytes plus a MIME type
// so Gemini can decode without inspecting the payload.
type Media struct {
	Data     []byte
	MimeType string
}

// IdentifyResult is what the response parser produces from a
// successful generateContent response.
type IdentifyResult struct {
	Title      string
	Notes      string
	Transcript string
	// Token usage from the API, when present in the response.
	// Used by the daemon's usage ledger so per-call spend is
	// attributable to the model that handled it.
	Model            string
	PromptTokens     int
	CandidatesTokens int
	TotalTokens      int
}

// EmbedResult holds the vector and token usage for an embedding call.
type EmbedResult struct {
	Vector []float32
	Model  string
	Tokens int
}

// FailoverApprover checks if a paid tier failover is approved for a specific operation.
type FailoverApprover interface {
	IsFailoverApproved(ctx context.Context, operation string) (bool, error)
}

// Client wraps a single HTTP client + model selection. Cheap to
// construct, no goroutines, safe for concurrent use.
type Client struct {
	HTTP             *http.Client
	Model            string
	BackoffMin       time.Duration
	PaidKey          string
	FailoverApprover FailoverApprover
	Operation        string
	exhausted        *exhaustedTracker
}

type exhaustedTracker struct {
	mu     sync.RWMutex
	models map[string]bool
}

func (c *Client) getBackoff(attempt int) time.Duration {
	min := c.BackoffMin
	if min == 0 {
		min = backoffMin
	}
	return time.Duration(1<<attempt) * min
}

// New returns a Client with sensible defaults — a 60s overall HTTP
// timeout that's bounded enough to fit inside the daemon's
// ExitTimeOut and generous enough for a heavy multi-image call.
func New() *Client {
	return &Client{
		HTTP:  &http.Client{Timeout: 60 * time.Second},
		Model: DefaultModel,
		exhausted: &exhaustedTracker{
			models: make(map[string]bool),
		},
	}
}

// WithFailover configures the client to fall back to a paid key if quota is exhausted.
func (c *Client) WithFailover(paidKey string, approver FailoverApprover, operation string) *Client {
	c.PaidKey = paidKey
	c.FailoverApprover = approver
	c.Operation = operation
	return c
}

// EmbedContent generates a vector embedding for the given text.
// Uses EmbeddingModel ("gemini-embedding-001").
func (c *Client) EmbedContent(ctx context.Context, apiKey string, text string) (EmbedResult, error) {
	if strings.TrimSpace(apiKey) == "" {
		return EmbedResult{}, ErrMissingKey
	}
	if strings.TrimSpace(text) == "" {
		return EmbedResult{}, errors.New("gemini: cannot embed empty text")
	}

	body := embedRequest{
		Model: "models/" + EmbeddingModel,
		Content: content{
			Parts: []part{{Text: text}},
		},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return EmbedResult{}, fmt.Errorf("encoding request: %w", err)
	}

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:embedContent?key=%s",
		EmbeddingModel, apiKey,
	)

	var lastErr error
	var decoded embedResponse

	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return EmbedResult{}, ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
		if err != nil {
			return EmbedResult{}, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("gemini http: %w", err)
			if IsTransient(lastErr) && attempt < 2 {
				backoff := c.getBackoff(attempt)
				select {
				case <-ctx.Done():
					return EmbedResult{}, ctx.Err()
				case <-time.After(backoff):
					continue
				}
			}
			break
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = &HTTPError{
				Status: resp.StatusCode,
				Body:   truncate(string(respBody), 800),
			}
			if isGlobalPermanentError(lastErr) {
				return EmbedResult{}, lastErr
			}
			if IsTransient(lastErr) && attempt < 2 {
				backoff := c.getBackoff(attempt)
				select {
				case <-ctx.Done():
					return EmbedResult{}, ctx.Err()
				case <-time.After(backoff):
					continue
				}
			}
			break
		}

		if err := json.Unmarshal(respBody, &decoded); err != nil {
			lastErr = fmt.Errorf("decoding response: %w", err)
			break // not transient
		}

		lastErr = nil
		break // success
	}

	if lastErr != nil {
		return EmbedResult{}, lastErr
	}

	res := EmbedResult{
		Vector: decoded.Embedding.Values,
		Model:  EmbeddingModel,
	}
	if decoded.Usage != nil {
		res.Tokens = decoded.Usage.TotalTokenCount
	}
	return res, nil
}

func (c *Client) executeGenerate(ctx context.Context, apiKey string, model string, buf []byte) (generateResponse, error) {
	apiVersion := "v1"
	if strings.Contains(model, "3.1") || strings.Contains(model, "2.0") || strings.Contains(model, "2.5") {
		apiVersion = "v1beta"
	}
	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/%s/models/%s:generateContent?key=%s",
		apiVersion, model, apiKey,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return generateResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return generateResponse{}, fmt.Errorf("gemini http: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return generateResponse{}, &HTTPError{
			Status: resp.StatusCode,
			Body:   truncate(string(respBody), 800),
		}
	}

	var decoded generateResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return generateResponse{}, fmt.Errorf("decoding response: %w (head: %s)", err, truncate(string(respBody), 200))
	}
	return decoded, nil
}

func normalizeModelName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "models/")
	if name == "gemini-3.1-flash" {
		return "gemini-3.1-flash-lite"
	}
	return name
}

func normalizeModelNames(names []string) []string {
	var normalized []string
	for _, n := range names {
		if norm := normalizeModelName(n); norm != "" {
			normalized = append(normalized, norm)
		}
	}
	return normalized
}

func (c *Client) primaryModel() string {
	// Programmatic override takes highest priority
	if c.Model != "" {
		return c.Model
	}
	
	// Operation-specific override
	cfg := config.Get()
	if c.Operation != "" {
		if opCfg, exists := cfg.Operations[c.Operation]; exists && opCfg.PrimaryModel != "" {
			return opCfg.PrimaryModel
		}
	}
	
	// Global primary model
	if cfg.PrimaryModel != "" {
		return cfg.PrimaryModel
	}
	
	return DefaultModel
}

func (c *Client) fallbackModels() []string {
	var models []string
	
	// Start with the resolved primary model
	primary := c.primaryModel()
	models = append(models, normalizeModelName(primary))
	
	// Find fallback list
	var cfgModels []string
	cfg := config.Get()
	if c.Operation != "" {
		if opCfg, exists := cfg.Operations[c.Operation]; exists && len(opCfg.AIModels) > 0 {
			cfgModels = opCfg.AIModels
		}
	}
	
	if len(cfgModels) == 0 {
		cfgModels = cfg.AIModels
	}
	
	for _, m := range cfgModels {
		norm := normalizeModelName(m)
		if norm == "" {
			continue
		}
		found := false
		for _, existing := range models {
			if existing == norm {
				found = true
				break
			}
		}
		if !found {
			models = append(models, norm)
		}
	}
	
	// Inject fallback chain for 2.5-pro and 2.5-flash models
	var injected []string
	for _, m := range models {
		injected = append(injected, m)
		if strings.Contains(m, "2.5-pro") || strings.Contains(m, "2.5-flash") {
			injected = append(injected, "gemini-3.1-flash", "gemini-2.5-flash-lite", "gemini-3.5-flash")
		}
	}
	
	// Deduplicate the final list
	var finalModels []string
	seen := make(map[string]bool)
	for _, m := range injected {
		if !seen[m] {
			seen[m] = true
			finalModels = append(finalModels, m)
		}
	}

	if len(finalModels) == 0 {
		finalModels = []string{DefaultModel}
	}
	return finalModels
}

// Identify sends one or more media items + a prompt to Gemini and
// returns the parsed title/notes/transcript. Caller supplies the
// API key — typically from internal/credentials.Load — so the
// gemini package itself stays storage-agnostic and testable.
func (c *Client) Identify(ctx context.Context, apiKey string, media []Media, prompt string) (IdentifyResult, error) {
	if strings.TrimSpace(apiKey) == "" {
		return IdentifyResult{}, ErrMissingKey
	}
	if len(media) == 0 {
		return IdentifyResult{}, errors.New("gemini: at least one media item required")
	}

	effectivePrompt := prompt
	if len(media) > 1 {
		effectivePrompt = MultiImageHint(len(media)) + prompt
	}

	parts := make([]part, 0, 1+len(media))
	parts = append(parts, part{Text: effectivePrompt})
	for _, m := range media {
		parts = append(parts, part{
			InlineData: &inlineData{
				MimeType: m.MimeType,
				Data:     base64.StdEncoding.EncodeToString(m.Data),
			},
		})
	}
	models := c.fallbackModels()
	var activeModels []string
	if c.exhausted != nil {
		c.exhausted.mu.RLock()
		for _, m := range models {
			if !c.exhausted.models[m] {
				activeModels = append(activeModels, m)
			}
		}
		c.exhausted.mu.RUnlock()
	} else {
		activeModels = models
	}
	if len(activeModels) == 0 {
		activeModels = models
	}

	var lastErr error
	var decoded generateResponse
	var finalModel string

	for _, model := range activeModels {
		if ctx.Err() != nil {
			return IdentifyResult{}, ctx.Err()
		}

		// Inject generationConfig specifically for 3.1 models
		body := generateRequest{Contents: []content{{Parts: parts}}}
		if strings.Contains(model, "3.1-pro") {
			body.GenerationConfig = &generationConfig{
				ThinkingConfig: &thinkingConfig{
					ThinkingLevel: "MEDIUM",
				},
			}
		}
		buf, err := json.Marshal(body)
		if err != nil {
			return IdentifyResult{}, fmt.Errorf("encoding request: %w", err)
		}

		for attempt := 0; attempt < 3; attempt++ {
			if ctx.Err() != nil {
				return IdentifyResult{}, ctx.Err()
			}
			decoded, lastErr = c.executeGenerate(ctx, apiKey, model, buf)
			if lastErr != nil {
				if isGlobalPermanentError(lastErr) {
					if IsQuotaErr(lastErr) {
						if c.exhausted != nil {
							c.exhausted.mu.Lock()
							c.exhausted.models[model] = true
							c.exhausted.mu.Unlock()
						}
						if c.PaidKey != "" && c.FailoverApprover != nil {
							op := c.Operation
							if op == "" {
								op = "identify"
							}
							approved, err := c.FailoverApprover.IsFailoverApproved(ctx, op)
							if err == nil && approved {
								primaryModel := models[0]
								body := generateRequest{Contents: []content{{Parts: parts}}}
								if strings.Contains(primaryModel, "3.1-pro") {
									body.GenerationConfig = &generationConfig{
										ThinkingConfig: &thinkingConfig{
											ThinkingLevel: "MEDIUM",
										},
									}
								}
								paidBuf, err := json.Marshal(body)
								if err == nil {
									var paidErr error
									decoded, paidErr = c.executeGenerate(ctx, c.PaidKey, primaryModel, paidBuf)
									if paidErr == nil {
										finalModel = primaryModel
										lastErr = nil
										break
									} else {
										lastErr = paidErr
									}
								}
							} else {
								return IdentifyResult{}, &ErrFailoverApprovalRequired{
									Operation: op,
								}
							}
						}
					}
					if lastErr != nil {
						return IdentifyResult{}, lastErr
					}
				}
				if IsTransient(lastErr) && attempt < 2 {
					backoff := c.getBackoff(attempt)
					select {
					case <-ctx.Done():
						return IdentifyResult{}, ctx.Err()
					case <-time.After(backoff):
						continue // Retry current model
					}
				}
				break // Not transient or last attempt -> break retry loop, switch to next model
			}

			// Treat empty response content as transient error
			if strings.TrimSpace(decoded.firstText()) == "" {
				lastErr = fmt.Errorf("gemini generate: empty response (model may be overloaded or content was filtered)")
				if attempt < 2 {
					backoff := c.getBackoff(attempt)
					select {
					case <-ctx.Done():
						return IdentifyResult{}, ctx.Err()
					case <-time.After(backoff):
						continue // Retry current model
					}
				}
				break // Switch to next model
			}

			finalModel = model
			break // Success
		}
		if lastErr == nil {
			break // Success
		}
	}
	if lastErr != nil {
		return IdentifyResult{}, fmt.Errorf("gemini identify failed after fallbacks: %w", lastErr)
	}

	result := IdentifyResult{Model: finalModel}
	if decoded.UsageMetadata != nil {
		result.PromptTokens = decoded.UsageMetadata.PromptTokenCount
		result.CandidatesTokens = decoded.UsageMetadata.CandidatesTokenCount
		result.TotalTokens = decoded.UsageMetadata.TotalTokenCount
	}

	text := decoded.firstText()
	parsed := Parse(text)
	result.Title = parsed.Title
	result.Notes = parsed.Notes
	result.Transcript = parsed.Transcript
	return result, nil
}

// QueryResult holds the text answer and token counts for a Query call.
type QueryResult struct {
	Answer           string
	Model            string
	PromptTokens     int
	CandidatesTokens int
}

// Query sends a question to Gemini about an item's content (text and/or
// media) and returns the answer. It includes previous context (like
// the existing title/notes) to ground the follow-up response.
func (c *Client) Query(ctx context.Context, apiKey string, contextInfo string, media []Media, question string) (QueryResult, error) {
	if strings.TrimSpace(apiKey) == "" {
		return QueryResult{}, ErrMissingKey
	}

	parts := make([]part, 0, 2+len(media))

	// Construct the prompt with context
	prompt := fmt.Sprintf("I have stashed the following content:\n\n%s\n\nMy question is: %s", contextInfo, question)
	parts = append(parts, part{Text: prompt})

	for _, m := range media {
		parts = append(parts, part{
			InlineData: &inlineData{
				MimeType: m.MimeType,
				Data:     base64.StdEncoding.EncodeToString(m.Data),
			},
		})
	}

	models := c.fallbackModels()
	var activeModels []string
	if c.exhausted != nil {
		c.exhausted.mu.RLock()
		for _, m := range models {
			if !c.exhausted.models[m] {
				activeModels = append(activeModels, m)
			}
		}
		c.exhausted.mu.RUnlock()
	} else {
		activeModels = models
	}
	if len(activeModels) == 0 {
		activeModels = models
	}

	var lastErr error
	var decoded generateResponse
	var finalModel string

	for _, model := range activeModels {
		if ctx.Err() != nil {
			return QueryResult{}, ctx.Err()
		}

		// Inject generationConfig specifically for 3.1 models
		body := generateRequest{Contents: []content{{Parts: parts}}}
		if strings.Contains(model, "3.1-pro") {
			body.GenerationConfig = &generationConfig{
				ThinkingConfig: &thinkingConfig{
					ThinkingLevel: "MEDIUM",
				},
			}
		}
		buf, err := json.Marshal(body)
		if err != nil {
			return QueryResult{}, fmt.Errorf("encoding request: %w", err)
		}
		for attempt := 0; attempt < 3; attempt++ {
			if ctx.Err() != nil {
				return QueryResult{}, ctx.Err()
			}
			decoded, lastErr = c.executeGenerate(ctx, apiKey, model, buf)
			if lastErr != nil {
				if isGlobalPermanentError(lastErr) {
					if IsQuotaErr(lastErr) {
						if c.exhausted != nil {
							c.exhausted.mu.Lock()
							c.exhausted.models[model] = true
							c.exhausted.mu.Unlock()
						}
						if c.PaidKey != "" && c.FailoverApprover != nil {
							op := c.Operation
							if op == "" {
								op = "query"
							}
							approved, err := c.FailoverApprover.IsFailoverApproved(ctx, op)
							if err == nil && approved {
								primaryModel := models[0]
								body := generateRequest{Contents: []content{{Parts: parts}}}
								if strings.Contains(primaryModel, "3.1-pro") {
									body.GenerationConfig = &generationConfig{
										ThinkingConfig: &thinkingConfig{
											ThinkingLevel: "MEDIUM",
										},
									}
								}
								paidBuf, err := json.Marshal(body)
								if err == nil {
									var paidErr error
									decoded, paidErr = c.executeGenerate(ctx, c.PaidKey, primaryModel, paidBuf)
									if paidErr == nil {
										finalModel = primaryModel
										lastErr = nil
										break
									} else {
										lastErr = paidErr
									}
								}
							} else {
								return QueryResult{}, &ErrFailoverApprovalRequired{
									Operation: op,
								}
							}
						}
					}
					if lastErr != nil {
						return QueryResult{}, lastErr
					}
				}
				if IsTransient(lastErr) && attempt < 2 {
					backoff := c.getBackoff(attempt)
					select {
					case <-ctx.Done():
						return QueryResult{}, ctx.Err()
					case <-time.After(backoff):
						continue // Retry current model
					}
				}
				break // Not transient or last attempt -> break retry loop, switch to next model
			}

			// Treat empty response content as transient error
			if strings.TrimSpace(decoded.firstText()) == "" {
				lastErr = fmt.Errorf("gemini query: empty response (model may be overloaded or content was filtered)")
				if attempt < 2 {
					backoff := c.getBackoff(attempt)
					select {
					case <-ctx.Done():
						return QueryResult{}, ctx.Err()
					case <-time.After(backoff):
						continue // Retry current model
					}
				}
				break // Switch to next model
			}

			finalModel = model
			break // Success
		}
		if lastErr == nil {
			break // Success
		}
	}
	if lastErr != nil {
		return QueryResult{}, fmt.Errorf("gemini query failed after fallbacks: %w", lastErr)
	}

	res := QueryResult{Model: finalModel}
	if decoded.UsageMetadata != nil {
		res.PromptTokens = decoded.UsageMetadata.PromptTokenCount
		res.CandidatesTokens = decoded.UsageMetadata.CandidatesTokenCount
	}

	res.Answer = decoded.firstText()
	return res, nil
}

// Fix sends text to Gemini to fix spelling and grammar.
func (c *Client) Fix(ctx context.Context, apiKey string, text string) (QueryResult, error) {
	prompt := "Fix spelling and grammar in the following text. Respond ONLY with the fixed text, no preamble or quotes:\n\n" + text
	return c.Query(ctx, apiKey, "", nil, prompt)
}

// Summary sends text to Gemini for a 1-sentence summary.
func (c *Client) Summary(ctx context.Context, apiKey string, text string) (QueryResult, error) {
	prompt := "Summarize the following text in exactly one concise sentence. Respond ONLY with the summary:\n\n" + text
	return c.Query(ctx, apiKey, "", nil, prompt)
}

// SuggestTags sends text to Gemini for tag suggestions.
func (c *Client) SuggestTags(ctx context.Context, apiKey string, text string) (QueryResult, error) {
	prompt := "Suggest exactly 3 relevant tags for the following content. Respond ONLY with the tags, space-separated, no # prefixes:\n\n" + text
	return c.Query(ctx, apiKey, "", nil, prompt)
}

// ----- Wire types --------------------------------------------------

type embedRequest struct {
	Model    string  `json:"model"`
	Content  content `json:"content"`
	TaskType string  `json:"taskType,omitempty"`
}

type embedResponse struct {
	Embedding embedding      `json:"embedding"`
	Usage     *usageMetadata `json:"usageMetadata,omitempty"`
}

type embedding struct {
	Values []float32 `json:"values"`
}

type generateRequest struct {
	Contents         []content         `json:"contents"`
	GenerationConfig *generationConfig `json:"generationConfig,omitempty"`
}

type generationConfig struct {
	ThinkingConfig *thinkingConfig `json:"thinkingConfig,omitempty"`
}

type thinkingConfig struct {
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
}


type content struct {
	Parts []part `json:"parts"`
}

type part struct {
	Text       string      `json:"text,omitempty"`
	InlineData *inlineData `json:"inline_data,omitempty"`
}

type inlineData struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

type generateResponse struct {
	Candidates    []candidate    `json:"candidates,omitempty"`
	UsageMetadata *usageMetadata `json:"usageMetadata,omitempty"`
}

type candidate struct {
	Content *candidateContent `json:"content,omitempty"`
}

type candidateContent struct {
	Parts []candidatePart `json:"parts,omitempty"`
}

type candidatePart struct {
	Text string `json:"text,omitempty"`
}

type usageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount int `json:"candidatesTokenCount,omitempty"`
	TotalTokenCount      int `json:"totalTokenCount,omitempty"`
}

func (r generateResponse) firstText() string {
	for _, c := range r.Candidates {
		if c.Content == nil {
			continue
		}
		for _, p := range c.Content.Parts {
			if t := strings.TrimSpace(p.Text); t != "" {
				return t
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func isGlobalPermanentError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrMissingKey) {
		return true
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		status := httpErr.Status
		body := strings.ToLower(httpErr.Body)
		if status == 400 || status == 401 || status == 403 {
			return true
		}
		if status == 429 {
			if strings.Contains(body, "budget-exceeded") ||
				strings.Contains(body, "budget exceeded") ||
				strings.Contains(body, "quota") ||
				strings.Contains(body, "free_tier") ||
				strings.Contains(body, "free-tier") ||
				strings.Contains(body, "billing") ||
				strings.Contains(body, "exhausted") ||
				strings.Contains(body, "exhaustion") {
				return true
			}
		}
	}
	return false
}

