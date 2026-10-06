package voxen

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// candidate is a provider that can serve a model, with the reasons (if any) its
// declarations do not cover this particular request.
type candidate struct {
	Provider Provider
	Key      string
	Missing  []string
	degraded bool
}

// missing lists what the request needs that the provider has not declared. A
// declaration with no list (for example no "formats") makes no claim either way.
func missing(p Provider, r Request) []string {
	var out []string
	need := func(capability string, ok bool) {
		if ok && !p.Has(capability) {
			out = append(out, "capability "+capability)
		}
	}
	need("speed", r.Speed != nil)
	need("normalize", r.Normalize)
	need("replace", len(r.Replace) > 0)
	need("latency", r.Latency > 0)
	need("sample_rate", r.SampleRate > 0)
	need("bitrate", r.BitRate > 0)
	need("format", r.Format != "")
	need("tags", tagPattern.MatchString(r.Text))
	if r.Format != "" && len(p.Formats) > 0 && !containsFold(p.Formats, r.Format) {
		out = append(out, "format "+r.Format)
	}
	if r.SampleRate > 0 && len(p.SampleRates) > 0 && !containsInt(p.SampleRates, r.SampleRate) {
		out = append(out, fmt.Sprintf("sample_rate %d", r.SampleRate))
	}
	if r.BitRate > 0 && len(p.BitRates) > 0 && !containsInt(p.BitRates, r.BitRate) {
		out = append(out, fmt.Sprintf("bit_rate %d", r.BitRate))
	}
	if l := strings.ToLower(r.Language); l != "" && l != "auto" && len(p.Languages) > 0 && !containsFold(p.Languages, l) {
		out = append(out, "language "+r.Language)
	}
	if r.Speed != nil && p.Limits.SpeedMax > 0 && (*r.Speed < p.Limits.SpeedMin || *r.Speed > p.Limits.SpeedMax) {
		out = append(out, fmt.Sprintf("speed %g outside declared %g..%g", *r.Speed, p.Limits.SpeedMin, p.Limits.SpeedMax))
	}
	return out
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// selectCandidates returns providers able to serve the model, best first. Providers
// whose declarations cover the request come before ones that do not. A pinned
// provider is used alone and never replaced.
func selectCandidates(c Config, health map[string]Health, model, pinned string, r Request, now time.Time) ([]candidate, error) {
	var out []candidate
	var skipped []string
	for name, p := range c.Providers {
		if pinned != "" && name != pinned {
			continue
		}
		if !p.SupportsModel(model) {
			if pinned != "" {
				return nil, fmt.Errorf("provider %s does not support model %s", name, model)
			}
			continue
		}
		key, err := Credential(p)
		if err != nil {
			if pinned != "" {
				return nil, err
			}
			skipped = append(skipped, err.Error())
			continue
		}
		out = append(out, candidate{Provider: p, Key: key, Missing: missing(p, r), degraded: health[name].degraded(fingerprint(key), now)})
	}
	if pinned != "" && len(out) == 0 {
		return nil, fmt.Errorf("unknown provider %q (see: voxen providers)", pinned)
	}
	if len(out) == 0 {
		msg := fmt.Sprintf("no usable provider for model %s", model)
		if len(skipped) > 0 {
			msg += ": " + strings.Join(skipped, "; ")
		}
		return nil, fmt.Errorf("%s", msg)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (len(a.Missing) == 0) != (len(b.Missing) == 0) {
			return len(a.Missing) == 0
		}
		if a.degraded != b.degraded {
			return !a.degraded
		}
		if a.Provider.Priority != b.Provider.Priority {
			return a.Provider.Priority < b.Provider.Priority
		}
		return a.Provider.Name < b.Provider.Name
	})
	return out, nil
}
