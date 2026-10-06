package voxen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"
)

const maxAudioBytes = 1 << 30

// Voice is one selectable voice as reported by a provider.
type Voice struct {
	ID       string `json:"voice_id"`
	Name     string `json:"name,omitempty"`
	Language string `json:"language,omitempty"`
	Gender   string `json:"gender,omitempty"`
}

// HTTPError is a non-success upstream answer. Channel errors are failures of the
// channel (auth, quota, outage) that another provider may not share; everything
// else is about the request itself and is never replayed elsewhere.
type HTTPError struct {
	Status  int
	Message string
	Channel bool
}

func (e *HTTPError) Error() string {
	if e.Status == 0 {
		return e.Message
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

func classify(status int, body []byte) bool {
	switch {
	case status == 401 || status == 402 || status == 403 || status == 408 || status == 429:
		return true
	case status >= 500:
		return true
	case status == 404:
		// A JSON 404 is an answer about the request (for example an unknown voice).
		// A bare "404 page not found" means the channel has no such endpoint.
		return !json.Valid(bytes.TrimSpace(body))
	}
	return false
}

func errorMessage(body []byte) string {
	var obj struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &obj) == nil && len(obj.Error) > 0 {
		var s string
		if json.Unmarshal(obj.Error, &s) == nil {
			return s
		}
		var detail struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(obj.Error, &detail) == nil && detail.Message != "" {
			return detail.Message
		}
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 300 {
		text = text[:300] + "..."
	}
	return text
}

func (p Provider) endpoint() string {
	if p.Kind == KindOpenAISpeech {
		return p.BaseURL + "/audio/speech"
	}
	return p.BaseURL + "/tts"
}

// speechBody builds the JSON body for one request. It sends exactly what the user
// asked for; it does not drop or rewrite values the provider has not declared.
func (p Provider) speechBody(model string, r Request) map[string]any {
	lang := r.Language
	if lang == "" {
		lang = "auto"
	}
	if p.Kind == KindOpenAISpeech {
		b := map[string]any{"model": model, "input": r.Text, "language": lang}
		if r.Voice != "" {
			b["voice"] = r.Voice
		}
		if r.Format != "" {
			b["response_format"] = r.Format
		}
		if r.Speed != nil {
			b["speed"] = *r.Speed
		}
		if r.Normalize {
			b["text_normalization"] = true
		}
		if len(r.Replace) > 0 {
			b["replace"] = r.Replace
		}
		if r.Latency > 0 {
			b["optimize_streaming_latency"] = r.Latency
		}
		if r.SampleRate > 0 || r.BitRate > 0 {
			b["output_format"] = outputFormat(r)
		}
		return b
	}
	b := map[string]any{"text": r.Text, "language": lang}
	if r.Voice != "" {
		b["voice_id"] = r.Voice
	}
	if r.Speed != nil {
		b["speed"] = *r.Speed
	}
	if r.Format != "" || r.SampleRate > 0 || r.BitRate > 0 {
		b["output_format"] = outputFormat(r)
	}
	if r.Normalize {
		b["text_normalization"] = true
	}
	if len(r.Replace) > 0 {
		b["replace"] = r.Replace
	}
	if r.Latency > 0 {
		b["optimize_streaming_latency"] = r.Latency
	}
	return b
}

func outputFormat(r Request) map[string]any {
	f := map[string]any{"codec": "mp3"}
	if r.Format != "" {
		f["codec"] = r.Format
	}
	if r.SampleRate > 0 {
		f["sample_rate"] = r.SampleRate
	}
	if r.BitRate > 0 {
		f["bit_rate"] = r.BitRate
	}
	return f
}

func (p Provider) newRequest(ctx context.Context, method, url string, body any, key string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "*/*")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (p Provider) do(req *http.Request) ([]byte, string, error) {
	resp, err := (&http.Client{Timeout: time.Duration(p.TimeoutSeconds) * time.Second}).Do(req)
	if err != nil {
		return nil, "", &HTTPError{Message: "request failed: " + sanitizeTransport(err), Channel: true}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioBytes))
	if err != nil {
		return nil, "", &HTTPError{Status: resp.StatusCode, Message: "reading response failed: " + sanitizeTransport(err), Channel: true}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", &HTTPError{Status: resp.StatusCode, Message: errorMessage(data), Channel: classify(resp.StatusCode, data)}
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// sanitizeTransport removes the URL from net/http errors; it can embed a host the
// user did not type, and never contains credentials, but keeps messages short.
func sanitizeTransport(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, "\": "); i >= 0 {
		msg = msg[i+3:]
	}
	return msg
}

// Synthesize performs one request and returns raw audio bytes plus content type.
func (p Provider) Synthesize(ctx context.Context, key, model string, r Request) ([]byte, string, error) {
	req, err := p.newRequest(ctx, http.MethodPost, p.endpoint(), p.speechBody(model, r), key)
	if err != nil {
		return nil, "", err
	}
	data, contentType, err := p.do(req)
	if err != nil {
		return nil, "", err
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "application/json" {
		// Timestamp envelope: {"audio": "<base64>", "content_type": "...", ...}.
		var env struct {
			Audio       string `json:"audio"`
			ContentType string `json:"content_type"`
		}
		if json.Unmarshal(data, &env) == nil && env.Audio != "" {
			audio, err := base64.StdEncoding.DecodeString(env.Audio)
			if err != nil {
				return nil, "", &HTTPError{Status: http.StatusOK, Message: "invalid audio envelope: " + err.Error()}
			}
			return audio, env.ContentType, nil
		}
		return nil, "", &HTTPError{Status: http.StatusOK, Message: "unexpected JSON response: " + errorMessage(data)}
	}
	if len(data) == 0 {
		return nil, "", &HTTPError{Status: http.StatusOK, Message: "empty audio response", Channel: true}
	}
	return data, contentType, nil
}

// voiceSpec interprets the optional "voices" setting: the string "live" asks the
// provider, an array lists voices (strings or voice objects).
func (p Provider) voiceSpec() (live bool, list []Voice, err error) {
	if len(p.Voices) == 0 {
		return false, nil, nil
	}
	var s string
	if json.Unmarshal(p.Voices, &s) == nil {
		if s != "live" {
			return false, nil, fmt.Errorf(`voices must be "live" or a list`)
		}
		return true, nil, nil
	}
	var names []string
	if json.Unmarshal(p.Voices, &names) == nil {
		for _, n := range names {
			list = append(list, Voice{ID: n})
		}
		return false, list, nil
	}
	if err := json.Unmarshal(p.Voices, &list); err != nil {
		return false, nil, fmt.Errorf(`voices must be "live" or a list of names or voice objects`)
	}
	return false, list, nil
}

// ListVoices returns the provider's voices: live from the upstream when the
// provider is configured that way, otherwise the configured list.
func (p Provider) ListVoices(ctx context.Context, key string) (voices []Voice, source string, err error) {
	live, list, err := p.voiceSpec()
	if err != nil {
		return nil, "", err
	}
	if !live {
		if len(list) == 0 {
			return nil, "", fmt.Errorf(`provider %s has no "voices" setting; set it to "live" or a list`, p.Name)
		}
		return list, "config", nil
	}
	if p.Kind != KindXAITTS {
		return nil, "", fmt.Errorf("provider %s: live voice listing is only available for kind %s", p.Name, KindXAITTS)
	}
	req, err := p.newRequest(ctx, http.MethodGet, p.BaseURL+"/tts/voices", nil, key)
	if err != nil {
		return nil, "", err
	}
	data, _, err := p.do(req)
	if err != nil {
		return nil, "", err
	}
	var payload struct {
		Voices []Voice `json:"voices"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, "", fmt.Errorf("provider %s returned an unreadable voice list: %w", p.Name, err)
	}
	sort.Slice(payload.Voices, func(i, j int) bool { return payload.Voices[i].ID < payload.Voices[j].ID })
	return payload.Voices, "live", nil
}
