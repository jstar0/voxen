package voxen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// App ties the configuration to the local state directory.
type App struct {
	Cfg    Config
	Home   string
	health *healthStore
}

func NewApp(cfg Config, home string) *App {
	return &App{Cfg: cfg, Home: home, health: newHealthStore(home)}
}

// SayOptions are the user's choices for one synthesis. Zero values mean "not
// given", so presets and configured defaults can fill them in.
type SayOptions struct {
	Text       string
	Model      string
	Provider   string
	Preset     string
	Voice      string
	Language   string
	Format     string
	Speed      *float64
	SampleRate int
	BitRate    int
	Latency    int
	Normalize  *bool
	Replace    map[string]string
	ChunkChars int
	Output     string
	Name       string
	Overwrite  bool
	Play       bool
	DryRun     bool
}

// Plan describes what a synthesis would do, for --dry-run.
type Plan struct {
	Model      string          `json:"model"`
	Request    Request         `json:"request"`
	Chars      int             `json:"chars"`
	Chunks     int             `json:"chunks"`
	Output     string          `json:"output"`
	Candidates []PlanCandidate `json:"candidates"`
}

type PlanCandidate struct {
	Provider string         `json:"provider"`
	Endpoint string         `json:"endpoint"`
	Missing  []string       `json:"missing,omitempty"`
	Degraded bool           `json:"degraded,omitempty"`
	Body     map[string]any `json:"body"`
}

// playAudio is replaced in tests.
var playAudio = func(path string) error { return exec.Command("afplay", path).Run() }

func overlay(dst *Preset, src Preset) {
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.Voice != "" {
		dst.Voice = src.Voice
	}
	if src.Language != "" {
		dst.Language = src.Language
	}
	if src.Speed != nil {
		dst.Speed = src.Speed
	}
	if src.Format != "" {
		dst.Format = src.Format
	}
	if src.SampleRate != 0 {
		dst.SampleRate = src.SampleRate
	}
	if src.BitRate != 0 {
		dst.BitRate = src.BitRate
	}
	if src.Normalize != nil {
		dst.Normalize = src.Normalize
	}
	if len(src.Replace) > 0 {
		merged := map[string]string{}
		for k, v := range dst.Replace {
			merged[k] = v
		}
		for k, v := range src.Replace {
			merged[k] = v
		}
		dst.Replace = merged
	}
}

// BuildRequest merges configured defaults, an optional preset and the explicit
// options (in that order of increasing priority) into one request.
func (a *App) BuildRequest(o SayOptions) (string, Request, error) {
	merged := Preset{}
	overlay(&merged, a.Cfg.Defaults)
	if o.Preset != "" {
		preset, ok := a.Cfg.Presets[o.Preset]
		if !ok {
			names := make([]string, 0, len(a.Cfg.Presets))
			for n := range a.Cfg.Presets {
				names = append(names, n)
			}
			return "", Request{}, fmt.Errorf("unknown preset %q (configured: %s)", o.Preset, strings.Join(sortedStrings(names), ", "))
		}
		overlay(&merged, preset)
	}
	overlay(&merged, Preset{Model: o.Model, Voice: o.Voice, Language: o.Language, Speed: o.Speed, Format: o.Format, SampleRate: o.SampleRate, BitRate: o.BitRate, Normalize: o.Normalize, Replace: o.Replace})
	model, err := ResolveModel(a.Cfg, merged.Model)
	if err != nil {
		return "", Request{}, err
	}
	text := strings.TrimSpace(o.Text)
	if text == "" {
		return "", Request{}, fmt.Errorf("no text to speak (give text, --text-file or stdin)")
	}
	r := Request{Text: text, Voice: merged.Voice, Language: merged.Language, Speed: merged.Speed, Format: strings.ToLower(merged.Format), SampleRate: merged.SampleRate, BitRate: merged.BitRate, Replace: merged.Replace, Latency: o.Latency}
	if merged.Normalize != nil {
		r.Normalize = *merged.Normalize
	}
	if r.Speed != nil && *r.Speed <= 0 {
		return "", Request{}, fmt.Errorf("speed must be positive")
	}
	if r.SampleRate < 0 || r.BitRate < 0 || r.Latency < 0 {
		return "", Request{}, fmt.Errorf("sample rate, bit rate and latency cannot be negative")
	}
	return model, r, nil
}

func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (a *App) chunkSize(p Provider, o SayOptions) int {
	if o.ChunkChars > 0 {
		return o.ChunkChars
	}
	return p.Limits.ChunkChars
}

// outputTarget decides where audio goes. With an output directory (or none) the
// file name is derived and its extension follows the audio actually returned.
func outputTarget(o SayOptions, voice string, now time.Time) (dir, file string, err error) {
	if o.Name != "" && (strings.ContainsAny(o.Name, `/\`) || o.Name == "." || o.Name == "..") {
		return "", "", fmt.Errorf("--name is a file name stem, not a path")
	}
	out := o.Output
	if out == "" {
		return ".", "", nil
	}
	out = expandPath(out)
	if info, err := os.Stat(out); (err == nil && info.IsDir()) || strings.HasSuffix(o.Output, "/") {
		return out, "", nil
	}
	if o.Name != "" {
		return "", "", fmt.Errorf("--name cannot be combined with an output file path; give a directory or drop --name")
	}
	if !o.Overwrite {
		if _, err := os.Stat(out); err == nil {
			return "", "", fmt.Errorf("%s already exists (use --overwrite)", out)
		}
	}
	return "", out, nil
}

func stem(o SayOptions, voice string, now time.Time) string {
	if o.Name != "" {
		return o.Name
	}
	if voice == "" {
		voice = "default"
	}
	clean := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '-'
	}, voice)
	return "voxen-" + now.Format("20060102-150405") + "-" + clean
}

func writeAudio(path string, data []byte, overwrite bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists (use --overwrite)", path)
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// Plan resolves everything Say would do without contacting any provider.
func (a *App) Plan(o SayOptions) (Plan, error) {
	model, r, err := a.BuildRequest(o)
	if err != nil {
		return Plan{}, err
	}
	cands, err := selectCandidates(a.Cfg, a.health.all(), model, o.Provider, r, time.Now())
	if err != nil {
		return Plan{}, err
	}
	dir, file, err := outputTarget(o, r.Voice, time.Now())
	if err != nil {
		return Plan{}, err
	}
	target := file
	if file == "" {
		target = filepath.Join(dir, stem(o, r.Voice, time.Now())+".<ext>")
	}
	plan := Plan{Model: model, Request: r, Chars: utf8.RuneCountInString(r.Text), Output: target}
	plan.Request.Text = ""
	for i, c := range cands {
		chunks := splitText(r.Text, a.chunkSize(c.Provider, o))
		if i == 0 {
			plan.Chunks = len(chunks)
		}
		first := r
		first.Text = chunks[0]
		plan.Candidates = append(plan.Candidates, PlanCandidate{Provider: c.Provider.Name, Endpoint: c.Provider.endpoint(), Missing: c.Missing, Degraded: c.degraded, Body: c.Provider.speechBody(model, first)})
	}
	return plan, nil
}

// Say synthesizes speech and writes the audio file.
func (a *App) Say(ctx context.Context, o SayOptions) (Result, error) {
	model, r, err := a.BuildRequest(o)
	if err != nil {
		return Result{}, err
	}
	cands, err := selectCandidates(a.Cfg, a.health.all(), model, o.Provider, r, time.Now())
	if err != nil {
		return Result{}, err
	}
	dir, file, err := outputTarget(o, r.Voice, time.Now())
	if err != nil {
		return Result{}, err
	}
	var attempts []Attempt
	var lastErr error
	for i, c := range cands {
		start := time.Now()
		audio, contentType, chunks, err := a.synthesize(ctx, c, model, r, a.chunkSize(c.Provider, o))
		attempt := Attempt{Provider: c.Provider.Name, OK: err == nil, Seconds: time.Since(start).Seconds()}
		var he *HTTPError
		if errors.As(err, &he) {
			attempt.Status = he.Status
		}
		if err != nil {
			attempt.Error = err.Error()
		}
		attempts = append(attempts, attempt)
		if err != nil {
			lastErr = err
			if errors.As(err, &he) && he.Channel {
				a.health.record(c.Provider.Name, fingerprint(c.Key), err)
				if i+1 < len(cands) {
					continue
				}
			}
			return Result{Attempts: attempts}, fmt.Errorf("%s: %w", c.Provider.Name, err)
		}
		a.health.record(c.Provider.Name, fingerprint(c.Key), nil)
		return a.finish(o, model, r, c, audio, contentType, chunks, attempts, dir, file)
	}
	return Result{Attempts: attempts}, lastErr
}

func (a *App) finish(o SayOptions, model string, r Request, c candidate, audio []byte, contentType string, chunks int, attempts []Attempt, dir, file string) (Result, error) {
	info := Inspect(audio, r)
	now := time.Now()
	path := file
	var warnings []string
	if path == "" {
		path = filepath.Join(dir, stem(o, r.Voice, now)+extension(info.Format))
	} else if ext := strings.ToLower(filepath.Ext(path)); oneOf(ext, ".mp3", ".wav", ".opus") && ext != extension(info.Format) {
		warnings = append(warnings, fmt.Sprintf("Output file is named %s but the audio is %s; the bytes were not converted", ext, info.Format))
	}
	if err := writeAudio(path, audio, o.Overwrite); err != nil {
		return Result{Attempts: attempts}, err
	}
	if len(c.Missing) > 0 {
		warnings = append(warnings, fmt.Sprintf("Provider %s has not declared support for: %s; the request was sent as given", c.Provider.Name, strings.Join(c.Missing, ", ")))
	}
	if len(attempts) > 1 {
		warnings = append(warnings, fmt.Sprintf("%d earlier provider(s) failed before %s answered", len(attempts)-1, c.Provider.Name))
	}
	if r.Format != "" && oneOf(info.Format, "mp3", "wav", "opus") && !strings.EqualFold(r.Format, info.Format) {
		warnings = append(warnings, fmt.Sprintf("Requested format %s but received %s; the provider did not apply it", r.Format, info.Format))
	}
	if r.SampleRate > 0 && !info.Assumed && info.SampleRate != 0 && info.SampleRate != r.SampleRate {
		warnings = append(warnings, fmt.Sprintf("Requested sample rate %d but received %d", r.SampleRate, info.SampleRate))
	}
	if r.BitRate > 0 && info.Format == "mp3" && info.BitRate != 0 && info.BitRate != r.BitRate {
		warnings = append(warnings, fmt.Sprintf("Requested bit rate %d but received %d", r.BitRate, info.BitRate))
	}
	if info.Assumed {
		warnings = append(warnings, "Headerless audio: duration and sample rate come from the request, not the stream")
	}
	requested := r
	requested.Text = ""
	res := Result{Path: path, Model: model, Provider: c.Provider.Name, Voice: r.Voice, Language: r.Language, Requested: requested, Format: info.Format, ContentType: contentType, SampleRate: info.SampleRate, BitRate: info.BitRate, Channels: info.Channels, Bytes: len(audio), DurationSeconds: info.Duration, DurationAssumed: info.Assumed, Chars: utf8.RuneCountInString(r.Text), Chunks: chunks, Attempts: attempts, Warnings: warnings, CreatedAt: now.UTC()}
	if o.Play {
		if err := playAudio(path); err != nil {
			res.Warnings = append(res.Warnings, "Playback failed: "+err.Error())
		}
	}
	return res, nil
}

// synthesize runs every chunk on one provider, a few at a time, and joins the audio.
func (a *App) synthesize(ctx context.Context, c candidate, model string, r Request, chunkChars int) ([]byte, string, int, error) {
	texts := splitText(r.Text, chunkChars)
	parts := make([][]byte, len(texts))
	types := make([]string, len(texts))
	workers := c.Provider.Limits.ChunkConcurrency
	if workers == 0 {
		workers = 3
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	slots := make(chan struct{}, workers)
	for i, text := range texts {
		wg.Add(1)
		slots <- struct{}{}
		go func(i int, text string) {
			defer wg.Done()
			defer func() { <-slots }()
			if ctx.Err() != nil {
				return
			}
			chunk := r
			chunk.Text = text
			data, contentType, err := c.Provider.Synthesize(ctx, c.Key, model, chunk)
			if err != nil {
				once.Do(func() { firstErr = err; cancel() })
				return
			}
			parts[i], types[i] = data, contentType
		}(i, text)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, "", len(texts), firstErr
	}
	audio, err := ConcatAudio(parts)
	if err != nil {
		return nil, "", len(texts), err
	}
	return audio, types[0], len(texts), nil
}
