package voxen

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Provider kinds. A kind decides the request protocol, not what a channel supports.
const (
	KindXAITTS       = "xai-tts"       // POST {base}/tts, GET {base}/tts/voices
	KindOpenAISpeech = "openai-speech" // POST {base}/audio/speech
)

var knownCapabilities = []string{"voices", "speed", "format", "sample_rate", "bitrate", "normalize", "replace", "tags", "latency", "long_text"}

func expandPath(s string) string {
	if strings.HasPrefix(s, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, s[2:])
	}
	return s
}

func ConfigPath() string {
	if p := os.Getenv("VOXEN_CONFIG"); p != "" {
		return expandPath(p)
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "voxen", "config.json")
}

func StateRoot() string {
	if p := os.Getenv("VOXEN_HOME"); p != "" {
		a, _ := filepath.Abs(expandPath(p))
		return a
	}
	if p := os.Getenv("XDG_STATE_HOME"); p != "" {
		return filepath.Join(p, "voxen")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "state", "voxen")
}

func oneOf(s string, options ...string) bool {
	for _, o := range options {
		if s == o {
			return true
		}
	}
	return false
}

func (p Provider) Has(capability string) bool {
	for _, c := range p.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

func (p Provider) SupportsModel(model string) bool {
	for _, m := range p.SupportedModels {
		if m == model {
			return true
		}
	}
	return false
}

func LoadConfig(path string) (Config, error) {
	var c Config
	f, err := os.Open(expandPath(path))
	if err != nil {
		return c, fmt.Errorf("read config: %w", err)
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, fmt.Errorf("decode config: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return c, fmt.Errorf("config must contain exactly one JSON object")
	}
	if c.Version != 1 {
		return c, fmt.Errorf("config version must be 1")
	}
	if c.Concurrency == 0 {
		c.Concurrency = 2
	}
	if c.Concurrency < 1 || c.Concurrency > 16 {
		return c, fmt.Errorf("concurrency must be 1..16")
	}
	if len(c.Providers) == 0 {
		return c, fmt.Errorf("no providers configured")
	}
	for name, p := range c.Providers {
		p.Name = name
		if !oneOf(p.Kind, KindXAITTS, KindOpenAISpeech) {
			return c, fmt.Errorf("provider %s: unsupported kind %q", name, p.Kind)
		}
		if p.APIKeyEnv == "" && p.APIKeyFile == "" {
			return c, fmt.Errorf("provider %s: credential reference required (api_key_env or api_key_file)", name)
		}
		if p.TimeoutSeconds == 0 {
			p.TimeoutSeconds = 600
		}
		if p.TimeoutSeconds < 1 || p.TimeoutSeconds > 3600 {
			return c, fmt.Errorf("provider %s: timeout_seconds must be 1..3600", name)
		}
		u, err := url.Parse(p.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, fmt.Errorf("provider %s: base_url must be an HTTP(S) URL without credentials, query or fragment", name)
		}
		p.BaseURL = strings.TrimRight(p.BaseURL, "/")
		if len(p.SupportedModels) == 0 {
			return c, fmt.Errorf("provider %s: supported_models required", name)
		}
		for _, m := range p.SupportedModels {
			if strings.TrimSpace(m) == "" || strings.Contains(m, "*") {
				return c, fmt.Errorf("provider %s: supported_models must be exact model names", name)
			}
		}
		for k := range p.Headers {
			if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "x-api-key") || strings.EqualFold(k, "api-key") {
				return c, fmt.Errorf("provider %s: authentication headers must use credential references", name)
			}
		}
		for _, cap := range p.Capabilities {
			if !contains(knownCapabilities, cap) {
				return c, fmt.Errorf("provider %s: unknown capability %s (known: %s)", name, cap, strings.Join(knownCapabilities, ", "))
			}
		}
		if p.Limits.SpeedMin < 0 || p.Limits.SpeedMax < p.Limits.SpeedMin || p.Limits.ChunkChars < 0 || p.Limits.ChunkConcurrency < 0 || p.Limits.ChunkConcurrency > 16 {
			return c, fmt.Errorf("provider %s: invalid limits", name)
		}
		if _, _, err := p.voiceSpec(); err != nil {
			return c, fmt.Errorf("provider %s: %w", name, err)
		}
		c.Providers[name] = p
	}
	if _, err := ResolveModel(c, ""); err != nil {
		return c, err
	}
	for alias := range c.Aliases {
		if _, err := ResolveModel(c, alias); err != nil {
			return c, err
		}
	}
	for name, preset := range c.Presets {
		if preset.Model != "" {
			if _, err := ResolveModel(c, preset.Model); err != nil {
				return c, fmt.Errorf("preset %s: %w", name, err)
			}
		}
	}
	return c, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// ResolveModel maps an alias or exact model name to an exact model name that at
// least one provider supports. An empty name selects default_model.
func ResolveModel(c Config, name string) (string, error) {
	if name == "" {
		name = c.DefaultModel
	}
	if name == "" {
		return "", fmt.Errorf("no model given and default_model is not configured")
	}
	if target, ok := c.Aliases[name]; ok {
		name = target
	}
	for _, p := range c.Providers {
		if p.SupportsModel(name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("model %q is not supported by any configured provider (see: voxen models)", name)
}

// ModelNames returns every exact model name any provider supports, sorted.
func ModelNames(c Config) []string {
	seen := map[string]bool{}
	for _, p := range c.Providers {
		for _, m := range p.SupportedModels {
			seen[m] = true
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Credential reads the explicit source. It never runs shell code or silently
// borrows an unrelated credential from another client.
func Credential(p Provider) (string, error) {
	var key string
	switch {
	case p.APIKeyFile != "":
		b, err := readPrivate(expandPath(p.APIKeyFile))
		if err != nil {
			return "", err
		}
		key = strings.TrimSpace(string(b))
	case p.EnvFile != "":
		b, err := readPrivate(expandPath(p.EnvFile))
		if err != nil {
			return "", err
		}
		s := bufio.NewScanner(strings.NewReader(string(b)))
		for s.Scan() {
			line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s.Text()), "export "))
			k, v, ok := strings.Cut(line, "=")
			if !ok || strings.TrimSpace(k) != p.APIKeyEnv {
				continue
			}
			v = strings.TrimSpace(v)
			if len(v) >= 2 && (v[0] == '\'' && v[len(v)-1] == '\'' || v[0] == '"' && v[len(v)-1] == '"') {
				v = v[1 : len(v)-1]
			}
			key = v
		}
		if err := s.Err(); err != nil {
			return "", err
		}
	default:
		key = os.Getenv(p.APIKeyEnv)
	}
	if key == "" {
		return "", fmt.Errorf("credential unavailable for provider %s (check its configured credential source)", p.Name)
	}
	if strings.ContainsAny(key, "\r\n") {
		return "", fmt.Errorf("credential for provider %s contains a newline", p.Name)
	}
	return key, nil
}

func readPrivate(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("credential source unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("credential source must be an owner-only regular file: %s", path)
	}
	return os.ReadFile(path)
}

// fingerprint lets health records expire after a key rotation without storing the key.
func fingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}
