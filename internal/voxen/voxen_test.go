package voxen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const secretKey = "sk-test-SECRET-do-not-leak"

type fakeUpstream struct {
	*httptest.Server
	mu       sync.Mutex
	bodies   []map[string]any
	paths    []string
	auth     []string
	respond  func(path string, body map[string]any) (status int, contentType string, payload []byte)
	requests int
}

func newUpstream(t *testing.T, respond func(path string, body map[string]any) (int, string, []byte)) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{respond: respond}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		u.mu.Lock()
		u.requests++
		u.bodies = append(u.bodies, body)
		u.paths = append(u.paths, r.Method+" "+r.URL.Path)
		u.auth = append(u.auth, r.Header.Get("Authorization"))
		u.mu.Unlock()
		status, ct, payload := u.respond(r.URL.Path, body)
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(status)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(u.Close)
	return u
}

func mp3Upstream(t *testing.T) *fakeUpstream {
	return newUpstream(t, func(path string, body map[string]any) (int, string, []byte) {
		if strings.HasSuffix(path, "/voices") {
			return 200, "application/json", []byte(`{"voices":[{"voice_id":"zed","name":"Zed","language":"multilingual","gender":"male"},{"voice_id":"amy","name":"Amy","language":"multilingual","gender":"female"}]}`)
		}
		return 200, "audio/mpeg", fakeMP3(5, 3)
	})
}

type testProvider struct {
	name         string
	up           *fakeUpstream
	kind         string
	capabilities []string
	extra        map[string]any
}

// writeConfig writes a config with the given providers and a private key file for each.
func writeConfig(t *testing.T, providers []testProvider, top map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	provs := map[string]any{}
	for i, p := range providers {
		keyFile := filepath.Join(dir, p.name+".key")
		if err := os.WriteFile(keyFile, []byte(secretKey+"-"+p.name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		kind := p.kind
		if kind == "" {
			kind = KindXAITTS
		}
		m := map[string]any{"kind": kind, "base_url": p.up.URL + "/v1", "api_key_file": keyFile, "supported_models": []string{"tts-1"}, "capabilities": p.capabilities, "priority": i + 1, "timeout_seconds": 30}
		for k, v := range p.extra {
			m[k] = v
		}
		provs[p.name] = m
	}
	cfg := map[string]any{"version": 1, "default_model": "tts-1", "providers": provs}
	for k, v := range top {
		cfg[k] = v
	}
	path := filepath.Join(dir, "config.json")
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestApp(t *testing.T, providers []testProvider, top map[string]any) *App {
	t.Helper()
	cfg, err := LoadConfig(writeConfig(t, providers, top))
	if err != nil {
		t.Fatal(err)
	}
	return NewApp(cfg, t.TempDir())
}

var allCaps = []string{"voices", "speed", "format", "sample_rate", "bitrate", "normalize", "replace", "tags", "latency", "long_text"}

func TestSayNativeSendsExactlyTheRequestedFields(t *testing.T) {
	up := mp3Upstream(t)
	a := newTestApp(t, []testProvider{{name: "p", up: up, capabilities: allCaps}}, nil)
	speed := 1.2
	norm := true
	out := t.TempDir()
	res, err := a.Say(context.Background(), SayOptions{Text: "你好", Voice: "rex", Language: "zh", Speed: &speed, Format: "wav", SampleRate: 16000, Normalize: &norm, Replace: map[string]string{"K8s": "kates"}, Latency: 2, Output: out + "/", Name: "greeting"})
	if err != nil {
		t.Fatal(err)
	}
	body := up.bodies[0]
	of, _ := body["output_format"].(map[string]any)
	rep, _ := body["replace"].(map[string]any)
	if up.paths[0] != "POST /v1/tts" || body["text"] != "你好" || body["voice_id"] != "rex" || body["language"] != "zh" || body["speed"] != 1.2 || body["text_normalization"] != true || body["optimize_streaming_latency"] != float64(2) || of["codec"] != "wav" || of["sample_rate"] != float64(16000) || rep["K8s"] != "kates" {
		t.Fatalf("path=%s body=%v", up.paths[0], body)
	}
	if _, has := body["model"]; has {
		t.Fatalf("native path must not send a model field: %v", body)
	}
	if up.auth[0] != "Bearer "+secretKey+"-p" {
		t.Fatalf("auth header = %q", up.auth[0])
	}
	if filepath.Base(res.Path) != "greeting.mp3" || res.Format != "mp3" || res.SampleRate != 44100 || res.Chunks != 1 || res.Provider != "p" {
		t.Fatalf("result = %+v", res)
	}
	if b, _ := os.ReadFile(res.Path); len(b) != 5*417 {
		t.Fatalf("file size %d", len(b))
	}
	if w := strings.Join(res.Warnings, "|"); len(res.Warnings) != 2 || !strings.Contains(w, "Requested format wav but received mp3") || !strings.Contains(w, "Requested sample rate 16000 but received 44100") {
		t.Fatalf("expected exactly the format and sample-rate mismatch warnings: %v", res.Warnings)
	}
}

func TestSayDefaultsLanguageToAutoAndOmitsUnsetFields(t *testing.T) {
	up := mp3Upstream(t)
	a := newTestApp(t, []testProvider{{name: "p", up: up}}, nil)
	if _, err := a.Say(context.Background(), SayOptions{Text: "hello", Output: t.TempDir() + "/"}); err != nil {
		t.Fatal(err)
	}
	body := up.bodies[0]
	if body["language"] != "auto" {
		t.Fatalf("language = %v", body["language"])
	}
	for _, k := range []string{"voice_id", "speed", "output_format", "replace", "text_normalization", "optimize_streaming_latency"} {
		if _, has := body[k]; has {
			t.Fatalf("%s must be omitted when not requested: %v", k, body)
		}
	}
}

func TestSayOpenAISpeechKind(t *testing.T) {
	up := mp3Upstream(t)
	a := newTestApp(t, []testProvider{{name: "p", up: up, kind: KindOpenAISpeech, capabilities: allCaps}}, nil)
	speed := 0.9
	if _, err := a.Say(context.Background(), SayOptions{Text: "hi", Voice: "eve", Language: "en", Format: "wav", Speed: &speed, Output: t.TempDir() + "/"}); err != nil {
		t.Fatal(err)
	}
	b := up.bodies[0]
	if up.paths[0] != "POST /v1/audio/speech" || b["model"] != "tts-1" || b["input"] != "hi" || b["voice"] != "eve" || b["language"] != "en" || b["response_format"] != "wav" || b["speed"] != 0.9 {
		t.Fatalf("path=%s body=%v", up.paths[0], b)
	}
}

func TestPresetsAndDefaultsMergeInOrder(t *testing.T) {
	up := mp3Upstream(t)
	speed := 0.8
	a := newTestApp(t, []testProvider{{name: "p", up: up, capabilities: allCaps}}, map[string]any{
		"defaults": map[string]any{"voice": "eve", "language": "auto", "speed": 1.0},
		"presets":  map[string]any{"narrator": map[string]any{"voice": "atlas", "language": "zh", "replace": map[string]string{"a": "b", "c": "d"}}},
	})
	_, r, err := a.BuildRequest(SayOptions{Text: "x", Preset: "narrator", Speed: &speed, Replace: map[string]string{"c": "override"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Voice != "atlas" || r.Language != "zh" || *r.Speed != 0.8 || r.Replace["a"] != "b" || r.Replace["c"] != "override" {
		t.Fatalf("request = %+v", r)
	}
	_, r, _ = a.BuildRequest(SayOptions{Text: "x", Voice: "rex"})
	if r.Voice != "rex" || r.Language != "auto" || *r.Speed != 1.0 {
		t.Fatalf("flag must beat default: %+v", r)
	}
	if _, _, err := a.BuildRequest(SayOptions{Text: "x", Preset: "nope"}); err == nil || !strings.Contains(err.Error(), "narrator") {
		t.Fatalf("unknown preset must list configured ones: %v", err)
	}
	if _, _, err := a.BuildRequest(SayOptions{Text: "  "}); err == nil {
		t.Fatalf("empty text must be rejected")
	}
}

func TestChunksAreSynthesizedAndJoinedInOrder(t *testing.T) {
	up := newUpstream(t, func(_ string, body map[string]any) (int, string, []byte) {
		text, _ := body["text"].(string)
		fill := byte(len([]rune(text)))
		return 200, "audio/wav", fakeWAV(24000, []byte{fill, fill})
	})
	a := newTestApp(t, []testProvider{{name: "p", up: up, capabilities: allCaps, extra: map[string]any{"limits": map[string]any{"chunk_chars": 6}}}}, nil)
	text := "一二三。四五六七。八九。"
	res, err := a.Say(context.Background(), SayOptions{Text: text, Output: t.TempDir() + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Chunks != 3 || up.requests != 3 {
		t.Fatalf("chunks=%d requests=%d", res.Chunks, up.requests)
	}
	data, _ := os.ReadFile(res.Path)
	if want := []byte{4, 4, 5, 5, 3, 3}; string(data[44:]) != string(want) {
		t.Fatalf("chunks out of order: %v", data[44:])
	}
	if res.Format != "wav" || res.Chars != len([]rune(text)) {
		t.Fatalf("result = %+v", res)
	}
}

func TestChannelFailureFailsOverAndLaterCallsPreferTheHealthyOne(t *testing.T) {
	bad := newUpstream(t, func(string, map[string]any) (int, string, []byte) {
		return 503, "application/json", []byte(`{"error":"overloaded"}`)
	})
	good := mp3Upstream(t)
	a := newTestApp(t, []testProvider{{name: "a-bad", up: bad, capabilities: allCaps}, {name: "b-good", up: good, capabilities: allCaps}}, nil)
	res, err := a.Say(context.Background(), SayOptions{Text: "hi", Output: t.TempDir() + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "b-good" || len(res.Attempts) != 2 || res.Attempts[0].OK || res.Attempts[0].Status != 503 || !strings.Contains(strings.Join(res.Warnings, " "), "earlier provider") {
		t.Fatalf("result = %+v", res)
	}
	bad.requests = 0
	res, err = a.Say(context.Background(), SayOptions{Text: "again", Output: t.TempDir() + "/"})
	if err != nil || res.Provider != "b-good" || bad.requests != 0 {
		t.Fatalf("degraded provider must be avoided: provider=%s badRequests=%d err=%v", res.Provider, bad.requests, err)
	}
	providers := a.Providers()
	if !providers[0].Degraded || providers[0].Health == nil || providers[1].Degraded {
		t.Fatalf("provider views = %+v", providers)
	}
}

func TestInputErrorsAreNotRetriedElsewhere(t *testing.T) {
	first := newUpstream(t, func(string, map[string]any) (int, string, []byte) {
		return 404, "application/json", []byte(`{"error":"TTS synthesis failed: Voice 'nope' not found"}`)
	})
	second := mp3Upstream(t)
	a := newTestApp(t, []testProvider{{name: "a", up: first, capabilities: allCaps}, {name: "b", up: second, capabilities: allCaps}}, nil)
	_, err := a.Say(context.Background(), SayOptions{Text: "hi", Voice: "nope", Output: t.TempDir() + "/"})
	if err == nil || !strings.Contains(err.Error(), "Voice 'nope' not found") {
		t.Fatalf("upstream message must reach the user: %v", err)
	}
	if second.requests != 0 {
		t.Fatalf("a bad voice must not be replayed on another provider")
	}
}

func TestBareNotFoundIsAChannelFailure(t *testing.T) {
	first := newUpstream(t, func(string, map[string]any) (int, string, []byte) {
		return 404, "text/plain", []byte("404 page not found")
	})
	second := mp3Upstream(t)
	a := newTestApp(t, []testProvider{{name: "a", up: first, capabilities: allCaps}, {name: "b", up: second, capabilities: allCaps}}, nil)
	res, err := a.Say(context.Background(), SayOptions{Text: "hi", Output: t.TempDir() + "/"})
	if err != nil || res.Provider != "b" {
		t.Fatalf("provider=%s err=%v", res.Provider, err)
	}
}

func TestPinnedProviderIsNeverReplaced(t *testing.T) {
	bad := newUpstream(t, func(string, map[string]any) (int, string, []byte) {
		return 429, "application/json", []byte(`{"error":"rate limited"}`)
	})
	good := mp3Upstream(t)
	a := newTestApp(t, []testProvider{{name: "a", up: bad, capabilities: allCaps}, {name: "b", up: good, capabilities: allCaps}}, nil)
	_, err := a.Say(context.Background(), SayOptions{Text: "hi", Provider: "a", Output: t.TempDir() + "/"})
	if err == nil || good.requests != 0 {
		t.Fatalf("pinned failure must stay a failure: err=%v goodRequests=%d", err, good.requests)
	}
	if _, err := a.Say(context.Background(), SayOptions{Text: "hi", Provider: "ghost", Output: t.TempDir() + "/"}); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown provider: %v", err)
	}
}

func TestDeclarationsGuideRoutingButNeverBlock(t *testing.T) {
	plain, rich := mp3Upstream(t), mp3Upstream(t)
	a := newTestApp(t, []testProvider{
		{name: "a-plain", up: plain, capabilities: []string{"voices"}},
		{name: "b-rich", up: rich, capabilities: allCaps, extra: map[string]any{"formats": []string{"mp3"}, "languages": []string{"auto", "zh"}, "limits": map[string]any{"speed_min": 0.7, "speed_max": 1.5}}},
	}, nil)
	speed := 1.1
	res, err := a.Say(context.Background(), SayOptions{Text: "hi", Speed: &speed, Language: "zh", Output: t.TempDir() + "/"})
	if err != nil || res.Provider != "b-rich" || plain.requests != 0 {
		t.Fatalf("a provider declaring speed must win: provider=%s err=%v", res.Provider, err)
	}
	// Nobody declares Klingon: the request is still sent, with a warning.
	// a-plain declares no language list, so it makes no claim and is not warned about.
	res, err = a.Say(context.Background(), SayOptions{Text: "hi", Language: "tlh", Output: t.TempDir() + "/"})
	if err != nil || res.Provider != "a-plain" || len(res.Warnings) != 0 {
		t.Fatalf("provider without a language list must not be warned: %+v err=%v", res, err)
	}
	// b-rich declares its languages; pinned to it, Klingon is sent anyway with a warning.
	res, err = a.Say(context.Background(), SayOptions{Text: "hi", Language: "tlh", Provider: "b-rich", Output: t.TempDir() + "/"})
	if err != nil || len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "language tlh") {
		t.Fatalf("undeclared language must warn, not block: %+v err=%v", res.Warnings, err)
	}
	if last := rich.bodies[len(rich.bodies)-1]; last["language"] != "tlh" {
		t.Fatalf("value must be sent unchanged: %v", last)
	}
	tooFast := 3.0
	res, err = a.Say(context.Background(), SayOptions{Text: "hi", Speed: &tooFast, Provider: "b-rich", Output: t.TempDir() + "/"})
	if err != nil || !strings.Contains(strings.Join(res.Warnings, " "), "speed 3 outside declared 0.7..1.5") {
		t.Fatalf("warnings = %v err=%v", res.Warnings, err)
	}
}

func TestModelsMustBeSupported(t *testing.T) {
	a := newTestApp(t, []testProvider{{name: "p", up: mp3Upstream(t)}}, map[string]any{"aliases": map[string]string{"fast": "tts-1"}})
	if m, err := ResolveModel(a.Cfg, "fast"); err != nil || m != "tts-1" {
		t.Fatalf("alias: %v %v", m, err)
	}
	if _, err := a.Say(context.Background(), SayOptions{Text: "x", Model: "other-model", Output: t.TempDir() + "/"}); err == nil || !strings.Contains(err.Error(), "other-model") {
		t.Fatalf("unsupported model: %v", err)
	}
}

func TestOutputNeverOverwritesUnlessAsked(t *testing.T) {
	a := newTestApp(t, []testProvider{{name: "p", up: mp3Upstream(t)}}, nil)
	dir := t.TempDir()
	target := filepath.Join(dir, "out.mp3")
	if _, err := a.Say(context.Background(), SayOptions{Text: "one", Output: target}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Say(context.Background(), SayOptions{Text: "two", Output: target}); err == nil || !strings.Contains(err.Error(), "--overwrite") {
		t.Fatalf("existing file: %v", err)
	}
	if _, err := a.Say(context.Background(), SayOptions{Text: "two", Output: target, Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Say(context.Background(), SayOptions{Text: "x", Output: dir + "/", Name: "../escape"}); err == nil {
		t.Fatalf("a name containing a path must be rejected")
	}
	res, err := a.Say(context.Background(), SayOptions{Text: "x", Output: filepath.Join(dir, "wrongext.wav")})
	if err != nil || len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "named .wav but the audio is mp3") {
		t.Fatalf("extension mismatch warning: %+v err=%v", res.Warnings, err)
	}
}

func TestTimestampEnvelopeIsUnwrapped(t *testing.T) {
	audio := fakeMP3(4, 9)
	up := newUpstream(t, func(string, map[string]any) (int, string, []byte) {
		env, _ := json.Marshal(map[string]any{"audio": base64.StdEncoding.EncodeToString(audio), "content_type": "audio/mpeg", "duration": 1})
		return 200, "application/json", env
	})
	a := newTestApp(t, []testProvider{{name: "p", up: up}}, nil)
	res, err := a.Say(context.Background(), SayOptions{Text: "hi", Output: t.TempDir() + "/"})
	if err != nil || res.Bytes != len(audio) || res.Format != "mp3" {
		t.Fatalf("result = %+v err=%v", res, err)
	}
}

func TestPlanDoesNotCallTheProvider(t *testing.T) {
	up := mp3Upstream(t)
	a := newTestApp(t, []testProvider{{name: "p", up: up, capabilities: allCaps, extra: map[string]any{"limits": map[string]any{"chunk_chars": 5}}}}, nil)
	plan, err := a.Plan(SayOptions{Text: "第一句话。第二句话。", Voice: "eve", Output: t.TempDir() + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if up.requests != 0 || plan.Chunks != 2 || len(plan.Candidates) != 1 || plan.Candidates[0].Body["voice_id"] != "eve" || plan.Request.Text != "" {
		t.Fatalf("plan = %+v requests=%d", plan, up.requests)
	}
}

func TestVoicesLiveAndConfigured(t *testing.T) {
	up := mp3Upstream(t)
	a := newTestApp(t, []testProvider{{name: "live", up: up, extra: map[string]any{"voices": "live"}}}, nil)
	res, err := a.Voices(context.Background(), "", "", "")
	if err != nil || res.Source != "live" || len(res.Voices) != 2 || res.Voices[0].ID != "amy" {
		t.Fatalf("live voices = %+v err=%v", res, err)
	}
	if res, _ = a.Voices(context.Background(), "", "", "Male"); len(res.Voices) != 1 || res.Voices[0].ID != "zed" {
		t.Fatalf("gender filter = %+v", res)
	}
	b := newTestApp(t, []testProvider{{name: "static", up: up, kind: KindOpenAISpeech, extra: map[string]any{"voices": []string{"alloy", "echo"}}}}, nil)
	if res, err = b.Voices(context.Background(), "", "", ""); err != nil || res.Source != "config" || len(res.Voices) != 2 {
		t.Fatalf("configured voices = %+v err=%v", res, err)
	}
	c := newTestApp(t, []testProvider{{name: "none", up: up}}, nil)
	if _, err = c.Voices(context.Background(), "", "", ""); err == nil {
		t.Fatalf("provider without a voice list must say so")
	}
}

func TestBatchNumbersFilesAndReportsPartialFailure(t *testing.T) {
	up := newUpstream(t, func(_ string, body map[string]any) (int, string, []byte) {
		if body["text"] == "boom" {
			return 400, "application/json", []byte(`{"error":"bad text"}`)
		}
		return 200, "audio/mpeg", fakeMP3(2, 1)
	})
	a := newTestApp(t, []testProvider{{name: "p", up: up, capabilities: allCaps}}, nil)
	items, err := ParseBatch(strings.NewReader("# comment\n\n第一行\nboom\n{\"text\":\"第三行\",\"name\":\"intro\",\"voice\":\"rex\",\"lang\":\"zh\"}\n"))
	if err != nil || len(items) != 3 {
		t.Fatalf("items = %+v err=%v", items, err)
	}
	dir := t.TempDir()
	results := a.Batch(context.Background(), items, SayOptions{Output: dir + "/", Voice: "eve"})
	if results[0].Error != "" || results[1].Error == "" || !strings.Contains(results[1].Error, "bad text") || results[2].Error != "" {
		t.Fatalf("results = %+v", results)
	}
	for _, name := range []string{"001.mp3", "intro.mp3"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	voices := map[string]bool{}
	for _, b := range up.bodies {
		if b["text"] == "第三行" {
			voices[b["voice_id"].(string)+"/"+b["language"].(string)] = true
		}
	}
	if !voices["rex/zh"] {
		t.Fatalf("per-line overrides were not applied: %v", up.bodies)
	}
	if _, err := ParseBatch(strings.NewReader("{\"text\":\"\"}\n")); err == nil {
		t.Fatalf("empty text in a JSON line must be rejected")
	}
	if _, err := ParseBatch(strings.NewReader("{\"txt\":\"typo\"}\n")); err == nil {
		t.Fatalf("unknown JSON field must be rejected")
	}
}

func TestConfigValidation(t *testing.T) {
	up := mp3Upstream(t)
	cases := map[string]func(m map[string]any){
		"unknown kind":       func(m map[string]any) { m["kind"] = "nope" },
		"unknown capability": func(m map[string]any) { m["capabilities"] = []string{"teleport"} },
		"auth header":        func(m map[string]any) { m["headers"] = map[string]string{"Authorization": "Bearer x"} },
		"wildcard model":     func(m map[string]any) { m["supported_models"] = []string{"tts-*"} },
		"bad url":            func(m map[string]any) { m["base_url"] = "https://user:pw@example.com/v1" },
		"no credential":      func(m map[string]any) { delete(m, "api_key_file") },
		"bad voices":         func(m map[string]any) { m["voices"] = "sometimes" },
	}
	for name, mutate := range cases {
		path := writeConfig(t, []testProvider{{name: "p", up: up}}, nil)
		var cfg map[string]any
		b, _ := os.ReadFile(path)
		_ = json.Unmarshal(b, &cfg)
		mutate(cfg["providers"].(map[string]any)["p"].(map[string]any))
		b, _ = json.Marshal(cfg)
		_ = os.WriteFile(path, b, 0o600)
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("%s: config should have been rejected", name)
		}
	}
	path := writeConfig(t, []testProvider{{name: "p", up: up}}, map[string]any{"surprise": true})
	if _, err := LoadConfig(path); err == nil {
		t.Errorf("unknown top-level field must be rejected")
	}
}

func TestCredentialFileMustBePrivateAndNothingLeaks(t *testing.T) {
	up := mp3Upstream(t)
	path := writeConfig(t, []testProvider{{name: "p", up: up, capabilities: allCaps}}, nil)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp(cfg, t.TempDir())
	res, err := a.Say(context.Background(), SayOptions{Text: "hi", Output: t.TempDir() + "/"})
	if err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	for _, v := range []any{res, a.Providers(), a.Models()} {
		b, _ := json.Marshal(v)
		dump.Write(b)
	}
	plan, _ := a.Plan(SayOptions{Text: "hi", Output: t.TempDir() + "/"})
	b, _ := json.Marshal(plan)
	dump.Write(b)
	health, _ := os.ReadFile(filepath.Join(a.Home, "health.json"))
	dump.Write(health)
	if strings.Contains(dump.String(), secretKey) {
		t.Fatalf("credential leaked into output or state")
	}
	provider := cfg.Providers["p"]
	if err := os.Chmod(expandPath(provider.APIKeyFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Credential(provider); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("group/world-readable credential must be refused: %v", err)
	}
	if err := os.WriteFile(expandPath(provider.APIKeyFile), []byte("a\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Credential(provider); err == nil {
		t.Fatalf("a multi-line credential must be refused")
	}
}

func TestProvidersReportMissingCredential(t *testing.T) {
	up := mp3Upstream(t)
	path := writeConfig(t, []testProvider{{name: "p", up: up}}, nil)
	cfg, _ := LoadConfig(path)
	_ = os.Remove(expandPath(cfg.Providers["p"].APIKeyFile))
	a := NewApp(cfg, t.TempDir())
	if v := a.Providers()[0]; v.Credential == "ok" {
		t.Fatalf("provider view = %+v", v)
	}
	if _, err := a.Say(context.Background(), SayOptions{Text: "hi", Output: t.TempDir() + "/"}); err == nil {
		t.Fatalf("no usable credential must fail clearly")
	}
}
