package voxen

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cliRun struct {
	stdout string
	err    error
}

func runCLI(t *testing.T, config string, stdin string, args ...string) cliRun {
	t.Helper()
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append([]string{"--config", config, "--home", t.TempDir()}, args...))
	err := cmd.Execute()
	return cliRun{out.String(), err}
}

func TestCLISayJSONArgumentsAndStdin(t *testing.T) {
	up := mp3Upstream(t)
	config := writeConfig(t, []testProvider{{name: "p", up: up, capabilities: allCaps}}, nil)
	dir := t.TempDir()
	r := runCLI(t, config, "", "say", "你好", "世界", "--voice", "rex", "--lang", "zh", "--speed", "1.1", "-o", dir+"/", "--name", "a", "--json")
	if r.err != nil {
		t.Fatal(r.err, r.stdout)
	}
	var res Result
	if err := json.Unmarshal([]byte(r.stdout), &res); err != nil || res.Voice != "rex" || res.Language != "zh" || filepath.Base(res.Path) != "a.mp3" {
		t.Fatalf("json = %q err=%v", r.stdout, err)
	}
	if up.bodies[0]["text"] != "你好 世界" || up.bodies[0]["speed"] != 1.1 {
		t.Fatalf("body = %v", up.bodies[0])
	}
	if res.Requested.Text != "" {
		t.Fatalf("result must not echo the whole text")
	}
	r = runCLI(t, config, "from stdin\n", "say", "--text-file", "-", "-o", dir+"/", "--name", "b")
	if r.err != nil || up.bodies[1]["text"] != "from stdin\n" && up.bodies[1]["text"] != "from stdin" {
		t.Fatalf("stdin: err=%v body=%v out=%s", r.err, up.bodies[1], r.stdout)
	}
	if !strings.Contains(r.stdout, "b.mp3") || !strings.Contains(r.stdout, "via p") {
		t.Fatalf("human output = %q", r.stdout)
	}
}

func TestCLINormalizeFalseOverridesPreset(t *testing.T) {
	up := mp3Upstream(t)
	config := writeConfig(t, []testProvider{{name: "p", up: up, capabilities: allCaps}}, map[string]any{"presets": map[string]any{"clean": map[string]any{"normalize": true, "voice": "atlas"}}})
	dir := t.TempDir()
	if r := runCLI(t, config, "", "say", "x", "--preset", "clean", "-o", dir+"/", "--name", "on"); r.err != nil {
		t.Fatal(r.err)
	}
	if r := runCLI(t, config, "", "say", "x", "--preset", "clean", "--normalize=false", "-o", dir+"/", "--name", "off"); r.err != nil {
		t.Fatal(r.err)
	}
	if up.bodies[0]["text_normalization"] != true || up.bodies[0]["voice_id"] != "atlas" {
		t.Fatalf("preset body = %v", up.bodies[0])
	}
	if _, has := up.bodies[1]["text_normalization"]; has {
		t.Fatalf("--normalize=false must override the preset: %v", up.bodies[1])
	}
}

func TestCLIReplaceFlagsAndFile(t *testing.T) {
	up := mp3Upstream(t)
	config := writeConfig(t, []testProvider{{name: "p", up: up, capabilities: allCaps}}, nil)
	file := filepath.Join(t.TempDir(), "readings.txt")
	_ = os.WriteFile(file, []byte("# readings\nKubernetes=koo-ber-net-eez\nnginx=engine x\n"), 0o644)
	r := runCLI(t, config, "", "say", "x", "--replace-file", file, "--replace", "nginx=en-jinx", "-o", t.TempDir()+"/")
	if r.err != nil {
		t.Fatal(r.err, r.stdout)
	}
	rep := up.bodies[0]["replace"].(map[string]any)
	if rep["Kubernetes"] != "koo-ber-net-eez" || rep["nginx"] != "en-jinx" {
		t.Fatalf("replace = %v", rep)
	}
	if r := runCLI(t, config, "", "say", "x", "--replace", "no-equals-sign"); r.err == nil {
		t.Fatalf("malformed override must be rejected")
	}
}

func TestCLIFailureJSONAndExitCode(t *testing.T) {
	up := newUpstream(t, func(string, map[string]any) (int, string, []byte) {
		return 400, "application/json", []byte(`{"error":"speed must be between 0.7 and 1.5"}`)
	})
	config := writeConfig(t, []testProvider{{name: "p", up: up, capabilities: allCaps}}, nil)
	r := runCLI(t, config, "", "say", "x", "--speed", "2", "--json", "-o", t.TempDir()+"/")
	var exit *ExitError
	if !errors.As(r.err, &exit) || exit.Code != 1 {
		t.Fatalf("err = %v", r.err)
	}
	var payload struct {
		Error    string    `json:"error"`
		Attempts []Attempt `json:"attempts"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &payload); err != nil || !strings.Contains(payload.Error, "speed must be between") || len(payload.Attempts) != 1 {
		t.Fatalf("stdout = %q err=%v", r.stdout, err)
	}
	r = runCLI(t, config, "", "say", "x", "--speed", "2", "-o", t.TempDir()+"/")
	if r.err == nil || !strings.Contains(r.err.Error(), "speed must be between") {
		t.Fatalf("plain error must carry the upstream message: %v", r.err)
	}
}

func TestCLIDryRunVoicesModelsProviders(t *testing.T) {
	up := mp3Upstream(t)
	config := writeConfig(t, []testProvider{{name: "p", up: up, capabilities: allCaps, extra: map[string]any{"voices": "live", "notes": []string{"declared note"}}}}, map[string]any{"aliases": map[string]string{"fast": "tts-1"}})
	r := runCLI(t, config, "", "say", "你好", "--dry-run")
	var plan Plan
	if r.err != nil || json.Unmarshal([]byte(r.stdout), &plan) != nil || up.requests != 0 || plan.Candidates[0].Provider != "p" {
		t.Fatalf("dry-run: err=%v out=%s requests=%d", r.err, r.stdout, up.requests)
	}
	r = runCLI(t, config, "", "voices", "--gender", "female")
	if r.err != nil || !strings.Contains(r.stdout, "amy") || strings.Contains(r.stdout, "zed") || !strings.Contains(r.stdout, "live") {
		t.Fatalf("voices: %v %q", r.err, r.stdout)
	}
	r = runCLI(t, config, "", "models", "--json")
	var models []ModelView
	if r.err != nil || json.Unmarshal([]byte(r.stdout), &models) != nil || models[0].Aliases[0] != "fast" || !models[0].Default {
		t.Fatalf("models: %v %q", r.err, r.stdout)
	}
	r = runCLI(t, config, "", "providers")
	if r.err != nil || !strings.Contains(r.stdout, "note: declared note") || !strings.Contains(r.stdout, "credential: ok") {
		t.Fatalf("providers: %v %q", r.err, r.stdout)
	}
}

func TestCLIBatchExitCodeAndFiles(t *testing.T) {
	up := newUpstream(t, func(_ string, body map[string]any) (int, string, []byte) {
		if body["text"] == "boom" {
			return 400, "application/json", []byte(`{"error":"bad"}`)
		}
		return 200, "audio/mpeg", fakeMP3(2, 1)
	})
	config := writeConfig(t, []testProvider{{name: "p", up: up, capabilities: allCaps}}, nil)
	dir := t.TempDir()
	r := runCLI(t, config, "one\nboom\ntwo\n", "batch", "-", "-o", dir)
	var exit *ExitError
	if !errors.As(r.err, &exit) || !strings.Contains(r.stdout, "2 of 3 succeeded") {
		t.Fatalf("err=%v out=%s", r.err, r.stdout)
	}
	for _, n := range []string{"001.mp3", "003.mp3"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	if r := runCLI(t, config, "x\n", "batch", "-", "--name", "n"); r.err == nil {
		t.Fatalf("--name must be rejected for batch")
	}
}
