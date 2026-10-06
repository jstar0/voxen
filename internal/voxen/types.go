package voxen

import (
	"encoding/json"
	"time"
)

// Config is the on-disk provider configuration (version 1).
type Config struct {
	Version      int                 `json:"version"`
	DefaultModel string              `json:"default_model"`
	Concurrency  int                 `json:"concurrency"`
	Defaults     Preset              `json:"defaults"`
	Aliases      map[string]string   `json:"aliases"`
	Presets      map[string]Preset   `json:"presets"`
	Providers    map[string]Provider `json:"providers"`
}

// Preset is a named bundle of synthesis settings owned by the user, not the upstream.
type Preset struct {
	Model      string            `json:"model,omitempty"`
	Voice      string            `json:"voice,omitempty"`
	Language   string            `json:"language,omitempty"`
	Speed      *float64          `json:"speed,omitempty"`
	Format     string            `json:"format,omitempty"`
	SampleRate int               `json:"sample_rate,omitempty"`
	BitRate    int               `json:"bit_rate,omitempty"`
	Normalize  *bool             `json:"normalize,omitempty"`
	Replace    map[string]string `json:"replace,omitempty"`
}

// Provider describes one upstream channel and what its owner declares it can do.
// Declarations guide routing and warnings; they never block a request, because a
// gateway's real behavior is judged from its response.
type Provider struct {
	Name            string            `json:"-"`
	Kind            string            `json:"kind"`
	BaseURL         string            `json:"base_url"`
	APIKeyEnv       string            `json:"api_key_env,omitempty"`
	APIKeyFile      string            `json:"api_key_file,omitempty"`
	EnvFile         string            `json:"env_file,omitempty"`
	SupportedModels []string          `json:"supported_models"`
	Capabilities    []string          `json:"capabilities,omitempty"`
	Formats         []string          `json:"formats,omitempty"`
	SampleRates     []int             `json:"sample_rates,omitempty"`
	BitRates        []int             `json:"bit_rates,omitempty"`
	Languages       []string          `json:"languages,omitempty"`
	Tags            []string          `json:"tags,omitempty"`
	Voices          json.RawMessage   `json:"voices,omitempty"`
	Limits          Limits            `json:"limits,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Priority        int               `json:"priority,omitempty"`
	TimeoutSeconds  int               `json:"timeout_seconds"`
	Notes           []string          `json:"notes,omitempty"`
}

// Limits are declared upstream limits. Zero means undeclared.
type Limits struct {
	SpeedMin         float64 `json:"speed_min,omitempty"`
	SpeedMax         float64 `json:"speed_max,omitempty"`
	ChunkChars       int     `json:"chunk_chars,omitempty"`
	ChunkConcurrency int     `json:"chunk_concurrency,omitempty"`
}

// Request is one synthesis request after presets and flags have been merged.
type Request struct {
	Text       string            `json:"text,omitempty"`
	Voice      string            `json:"voice,omitempty"`
	Language   string            `json:"language,omitempty"`
	Speed      *float64          `json:"speed,omitempty"`
	Format     string            `json:"format,omitempty"`
	SampleRate int               `json:"sample_rate,omitempty"`
	BitRate    int               `json:"bit_rate,omitempty"`
	Normalize  bool              `json:"normalize,omitempty"`
	Replace    map[string]string `json:"replace,omitempty"`
	Latency    int               `json:"latency,omitempty"`
}

type Attempt struct {
	Provider string  `json:"provider"`
	OK       bool    `json:"ok"`
	Status   int     `json:"status,omitempty"`
	Error    string  `json:"error,omitempty"`
	Seconds  float64 `json:"seconds"`
}

// Result describes what was requested and what the upstream actually returned.
type Result struct {
	Path            string    `json:"path"`
	Model           string    `json:"model"`
	Provider        string    `json:"provider"`
	Voice           string    `json:"voice,omitempty"`
	Language        string    `json:"language,omitempty"`
	Requested       Request   `json:"requested"`
	Format          string    `json:"format"`
	ContentType     string    `json:"content_type,omitempty"`
	SampleRate      int       `json:"sample_rate,omitempty"`
	BitRate         int       `json:"bit_rate,omitempty"`
	Channels        int       `json:"channels,omitempty"`
	Bytes           int       `json:"bytes"`
	DurationSeconds float64   `json:"duration_seconds"`
	DurationAssumed bool      `json:"duration_assumed,omitempty"`
	Chars           int       `json:"chars"`
	Chunks          int       `json:"chunks"`
	Attempts        []Attempt `json:"attempts"`
	Warnings        []string  `json:"warnings,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}
