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
	"time"
)

// DefaultModel matches the Mac client's default. Override per call
// via Client.Model if a fallback chain needs to route some calls
// to Pro etc.
const DefaultModel = "gemini-2.5-flash"

// Image is the input shape for identify — bytes plus a MIME type
// so Gemini can decode without inspecting the payload.
type Image struct {
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

// Client wraps a single HTTP client + model selection. Cheap to
// construct, no goroutines, safe for concurrent use.
type Client struct {
	HTTP  *http.Client
	Model string
}

// New returns a Client with sensible defaults — a 60s overall HTTP
// timeout that's bounded enough to fit inside the daemon's
// ExitTimeOut and generous enough for a heavy multi-image call.
func New() *Client {
	return &Client{
		HTTP:  &http.Client{Timeout: 60 * time.Second},
		Model: DefaultModel,
	}
}

// Identify sends one or more images + a prompt to Gemini and
// returns the parsed title/notes/transcript. Caller supplies the
// API key — typically from internal/credentials.Load — so the
// gemini package itself stays storage-agnostic and testable.
func (c *Client) Identify(ctx context.Context, apiKey string, images []Image, prompt string) (IdentifyResult, error) {
	if strings.TrimSpace(apiKey) == "" {
		return IdentifyResult{}, ErrMissingKey
	}
	if len(images) == 0 {
		return IdentifyResult{}, errors.New("gemini: at least one image required")
	}

	effectivePrompt := prompt
	if len(images) > 1 {
		effectivePrompt = MultiImageHint(len(images)) + prompt
	}

	parts := make([]part, 0, 1+len(images))
	parts = append(parts, part{Text: effectivePrompt})
	for _, img := range images {
		parts = append(parts, part{
			InlineData: &inlineData{
				MimeType: img.MimeType,
				Data:     base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	body := generateRequest{Contents: []content{{Parts: parts}}}
	buf, err := json.Marshal(body)
	if err != nil {
		return IdentifyResult{}, fmt.Errorf("encoding request: %w", err)
	}

	model := c.Model
	if model == "" {
		model = DefaultModel
	}
	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		model, apiKey,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return IdentifyResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return IdentifyResult{}, fmt.Errorf("gemini http: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return IdentifyResult{}, &HTTPError{
			Status: resp.StatusCode,
			Body:   truncate(string(respBody), 800),
		}
	}

	var decoded generateResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return IdentifyResult{}, fmt.Errorf("decoding response: %w (head: %s)", err, truncate(string(respBody), 200))
	}

	result := IdentifyResult{Model: model}
	if decoded.UsageMetadata != nil {
		result.PromptTokens = decoded.UsageMetadata.PromptTokenCount
		result.CandidatesTokens = decoded.UsageMetadata.CandidatesTokenCount
		result.TotalTokens = decoded.UsageMetadata.TotalTokenCount
	}

	text := decoded.firstText()
	if strings.TrimSpace(text) == "" {
		return result, ErrEmptyResponse
	}

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
// images) and returns the answer. It includes previous context (like
// the existing title/notes) to ground the follow-up response.
func (c *Client) Query(ctx context.Context, apiKey string, contextInfo string, images []Image, question string) (QueryResult, error) {
	if strings.TrimSpace(apiKey) == "" {
		return QueryResult{}, ErrMissingKey
	}

	parts := make([]part, 0, 2+len(images))

	// Construct the prompt with context
	prompt := fmt.Sprintf("I have stashed the following content:\n\n%s\n\nMy question is: %s", contextInfo, question)
	parts = append(parts, part{Text: prompt})

	for _, img := range images {
		parts = append(parts, part{
			InlineData: &inlineData{
				MimeType: img.MimeType,
				Data:     base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}

	body := generateRequest{Contents: []content{{Parts: parts}}}
	buf, err := json.Marshal(body)
	if err != nil {
		return QueryResult{}, fmt.Errorf("encoding request: %w", err)
	}

	model := c.Model
	if model == "" {
		model = DefaultModel
	}
	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		model, apiKey,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return QueryResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return QueryResult{}, fmt.Errorf("gemini http: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, &HTTPError{
			Status: resp.StatusCode,
			Body:   truncate(string(respBody), 800),
		}
	}

	var decoded generateResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return QueryResult{}, fmt.Errorf("decoding response: %w", err)
	}

	res := QueryResult{Model: model}
	if decoded.UsageMetadata != nil {
		res.PromptTokens = decoded.UsageMetadata.PromptTokenCount
		res.CandidatesTokens = decoded.UsageMetadata.CandidatesTokenCount
	}

	text := decoded.firstText()
	if strings.TrimSpace(text) == "" {
		return res, ErrEmptyResponse
	}

	res.Answer = text
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

type generateRequest struct {
	Contents []content `json:"contents"`
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
