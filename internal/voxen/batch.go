package voxen

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// BatchItem is one line of a batch file. Plain lines are just text; lines that
// start with "{" are JSON objects that may override the shared options.
type BatchItem struct {
	Text      string            `json:"text"`
	Name      string            `json:"name,omitempty"`
	Preset    string            `json:"preset,omitempty"`
	Model     string            `json:"model,omitempty"`
	Voice     string            `json:"voice,omitempty"`
	Language  string            `json:"lang,omitempty"`
	Format    string            `json:"format,omitempty"`
	Speed     *float64          `json:"speed,omitempty"`
	Normalize *bool             `json:"normalize,omitempty"`
	Replace   map[string]string `json:"replace,omitempty"`
}

// BatchResult pairs an input line with its outcome.
type BatchResult struct {
	Index  int     `json:"index"`
	Name   string  `json:"name"`
	Result *Result `json:"result,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// ParseBatch reads a batch file. Blank lines and lines starting with # are skipped.
func ParseBatch(r io.Reader) ([]BatchItem, error) {
	var items []BatchItem
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 1<<20), 16<<20)
	for n := 1; s.Scan(); n++ {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "{") {
			var item BatchItem
			d := json.NewDecoder(strings.NewReader(line))
			d.DisallowUnknownFields()
			if err := d.Decode(&item); err != nil {
				return nil, fmt.Errorf("line %d: %w", n, err)
			}
			if strings.TrimSpace(item.Text) == "" {
				return nil, fmt.Errorf("line %d: missing text", n)
			}
			items = append(items, item)
			continue
		}
		items = append(items, BatchItem{Text: line})
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("batch file has no text lines")
	}
	return items, nil
}

// Batch synthesizes every item into the output directory, a few at a time.
// Items without a name are numbered 001, 002, … by their position.
func (a *App) Batch(ctx context.Context, items []BatchItem, base SayOptions) []BatchResult {
	results := make([]BatchResult, len(items))
	slots := make(chan struct{}, a.Cfg.Concurrency)
	var wg sync.WaitGroup
	width := len(fmt.Sprint(len(items)))
	if width < 3 {
		width = 3
	}
	for i, item := range items {
		o := base
		o.Text = item.Text
		o.Name = item.Name
		if o.Name == "" {
			o.Name = fmt.Sprintf("%0*d", width, i+1)
		}
		if item.Preset != "" {
			o.Preset = item.Preset
		}
		if item.Model != "" {
			o.Model = item.Model
		}
		if item.Voice != "" {
			o.Voice = item.Voice
		}
		if item.Language != "" {
			o.Language = item.Language
		}
		if item.Format != "" {
			o.Format = item.Format
		}
		if item.Speed != nil {
			o.Speed = item.Speed
		}
		if item.Normalize != nil {
			o.Normalize = item.Normalize
		}
		if len(item.Replace) > 0 {
			merged := map[string]string{}
			for k, v := range base.Replace {
				merged[k] = v
			}
			for k, v := range item.Replace {
				merged[k] = v
			}
			o.Replace = merged
		}
		o.Play = false
		results[i] = BatchResult{Index: i + 1, Name: o.Name}
		wg.Add(1)
		slots <- struct{}{}
		go func(i int, o SayOptions) {
			defer wg.Done()
			defer func() { <-slots }()
			res, err := a.Say(ctx, o)
			if err != nil {
				results[i].Error = err.Error()
				return
			}
			results[i].Result = &res
		}(i, o)
	}
	wg.Wait()
	return results
}
