package voxen

import (
	"context"
	"sort"
	"strings"
	"time"
)

// ModelView is one exact model name and where it can be served.
type ModelView struct {
	Model     string   `json:"model"`
	Aliases   []string `json:"aliases,omitempty"`
	Default   bool     `json:"default,omitempty"`
	Providers []string `json:"providers"`
}

// ProviderView is everything a client needs to know about a provider, as declared
// in configuration plus recent health. It never contains credentials.
type ProviderView struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	BaseURL      string   `json:"base_url"`
	Models       []string `json:"models"`
	Capabilities []string `json:"capabilities,omitempty"`
	Formats      []string `json:"formats,omitempty"`
	SampleRates  []int    `json:"sample_rates,omitempty"`
	BitRates     []int    `json:"bit_rates,omitempty"`
	Languages    []string `json:"languages,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Voices       string   `json:"voices,omitempty"` // "live", "list" or empty
	Limits       Limits   `json:"limits,omitempty"`
	Priority     int      `json:"priority,omitempty"`
	Credential   string   `json:"credential"` // "ok" or the reason it is unusable
	Health       *Health  `json:"health,omitempty"`
	Degraded     bool     `json:"degraded,omitempty"`
	Notes        []string `json:"notes,omitempty"`
}

func (a *App) Models() []ModelView {
	var out []ModelView
	def, _ := ResolveModel(a.Cfg, "")
	for _, model := range ModelNames(a.Cfg) {
		v := ModelView{Model: model, Default: model == def}
		for alias, target := range a.Cfg.Aliases {
			if target == model {
				v.Aliases = append(v.Aliases, alias)
			}
		}
		sort.Strings(v.Aliases)
		for name, p := range a.Cfg.Providers {
			if p.SupportsModel(model) {
				v.Providers = append(v.Providers, name)
			}
		}
		sort.Strings(v.Providers)
		out = append(out, v)
	}
	return out
}

func (a *App) Providers() []ProviderView {
	health := a.health.all()
	names := make([]string, 0, len(a.Cfg.Providers))
	for n := range a.Cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []ProviderView
	for _, name := range names {
		p := a.Cfg.Providers[name]
		v := ProviderView{Name: name, Kind: p.Kind, BaseURL: p.BaseURL, Models: p.SupportedModels, Capabilities: p.Capabilities, Formats: p.Formats, SampleRates: p.SampleRates, BitRates: p.BitRates, Languages: p.Languages, Tags: p.Tags, Limits: p.Limits, Priority: p.Priority, Notes: p.Notes, Credential: "ok"}
		if live, list, _ := p.voiceSpec(); live {
			v.Voices = "live"
		} else if len(list) > 0 {
			v.Voices = "list"
		}
		key, err := Credential(p)
		if err != nil {
			v.Credential = err.Error()
		} else if h, ok := health[name]; ok && h.Fingerprint == fingerprint(key) {
			v.Degraded = h.degraded(fingerprint(key), time.Now())
			h.Fingerprint = ""
			v.Health = &h
		}
		out = append(out, v)
	}
	return out
}

// VoicesResult is a provider's voice list and where it came from.
type VoicesResult struct {
	Provider string  `json:"provider"`
	Source   string  `json:"source"`
	Voices   []Voice `json:"voices"`
}

// Voices lists voices from the pinned provider, or from the first provider that
// serves the model and offers a voice list.
func (a *App) Voices(ctx context.Context, model, pinned, gender string) (VoicesResult, error) {
	resolved, err := ResolveModel(a.Cfg, model)
	if err != nil {
		return VoicesResult{}, err
	}
	cands, err := selectCandidates(a.Cfg, a.health.all(), resolved, pinned, Request{}, time.Now())
	if err != nil {
		return VoicesResult{}, err
	}
	var lastErr error
	for _, c := range cands {
		voices, source, err := c.Provider.ListVoices(ctx, c.Key)
		if err != nil {
			lastErr = err
			if pinned != "" {
				return VoicesResult{}, err
			}
			continue
		}
		if gender != "" {
			filtered := voices[:0:0]
			for _, v := range voices {
				if strings.EqualFold(v.Gender, gender) {
					filtered = append(filtered, v)
				}
			}
			voices = filtered
		}
		return VoicesResult{Provider: c.Provider.Name, Source: source, Voices: voices}, nil
	}
	return VoicesResult{}, lastErr
}
