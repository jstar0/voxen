# voxen

A Go CLI plus an agent skill for text-to-speech. Pick a voice and language, and
voxen sends the text to a configured provider and saves the audio locally. No
MCP server or always-running daemon is required. It is the speech counterpart of
[imagen](https://github.com/jstar0/imagen) and follows the same ideas: providers
and their abilities live in configuration, the CLI reports them, and the skill
never hardcodes what an upstream can do.

Currently supported provider kinds:

| kind | protocol |
| --- | --- |
| `xai-tts` | xAI's native `POST /tts` and `GET /tts/voices` (also served by many xAI-compatible gateways) |
| `openai-speech` | OpenAI-style `POST /audio/speech` |

## Install

Requires Go 1.26 to build. Supports macOS and Linux (`--play` uses macOS `afplay`).
The installed CLI has no Python or Node dependency.

```sh
git clone git@github.com:jstar0/voxen.git
cd voxen
bash scripts/install.sh
```

This installs `~/.local/bin/voxen` and backs up what it replaces. Existing
configuration is preserved. `RUNTIME_OUT`, `BIN_DIR`, `CONFIG_DIR` and `BACKUP_DIR`
may be overridden. `VOXEN_CONFIG_SOURCE` explicitly links an existing configuration
file into the config directory; it is never inferred.

Install or symlink `skills/voxen` into your agent's skill directory. The CLI is
also fully usable without any AI client.

## Configure

Copy `config.example.json` to `~/.config/voxen/config.json` (`VOXEN_CONFIG` or
`--config` selects another file). Credentials are referenced, never stored in the
configuration: `api_key_env`, or `api_key_file` / `env_file` pointing at an owner-only
(mode 0600) file. A missing file never falls back to an unrelated inherited key.

**The example describes protocols and published API values, not verified access
for your account.** No credentials or personal endpoints are included.

Top level: `default_model`, `concurrency` (parallel batch items, 1..16), `defaults`
(voice, language, speed, format, … applied to every request), `aliases` (names for
exact models) and `presets` (named bundles you choose with `--preset`).

Each provider declares what it serves:

| Setting | Purpose |
| --- | --- |
| `kind`, `base_url`, credential reference | how to reach it |
| `supported_models` | exact model names it serves (no wildcards) |
| `capabilities` | any of `voices`, `speed`, `format`, `sample_rate`, `bitrate`, `normalize`, `replace`, `tags`, `latency`, `long_text` |
| `formats`, `sample_rates`, `bit_rates`, `languages`, `tags` | optional lists; an empty list makes no claim |
| `voices` | `"live"` (ask the provider) or a list of names or `{voice_id, name, language, gender}` objects |
| `limits` | `speed_min`, `speed_max`, `chunk_chars` (split longer text), `chunk_concurrency` |
| `priority` | lower is preferred when everything else is equal |
| `headers` | extra non-secret headers (authentication headers are rejected) |
| `notes` | known channel quirks, shown to clients |

Declarations guide routing and warnings. They never block a request: a gateway may
accept or ignore any value, which is judged from the response.

## Use

```sh
voxen say "你好，世界" --voice eve --lang zh -o ./audio/ --name hello
voxen say --text-file script.txt --preset narrator --speed 0.9 -o ./audio/
echo "Hello" | voxen say --format wav --sample-rate 16000 -o hello.wav
voxen say "K8s is great" --replace "K8s=kubernetes" --normalize --json

voxen batch lines.txt -o ./audio/ --voice rex     # numbered 001, 002, …
voxen voices --gender female                      # live from the provider
voxen models --json
voxen providers --json                            # declarations, credential and health
voxen say "hi" --dry-run                          # plan and request body, no network
```

Values such as voice, language, format and speed are sent to the provider as given.
Speech tags (pauses, laughter, whispering, …) are ordinary text in the input; use the
forms the provider declares in `tags`. Long text is split at sentence boundaries into
`chunk_chars`-sized requests (a speech wrapper that crosses a boundary is closed and
reopened), synthesized a few at a time, and joined: MP3 and raw audio by concatenation,
WAV with a rewritten header, Opus as one re-linked Ogg stream.

`batch` reads one text per line; a line starting with `{` is a JSON object with
`text` and optional `name`, `preset`, `model`, `voice`, `lang`, `format`, `speed`,
`normalize`, `replace`. Blank lines and `#` comments are skipped. It exits 1 if any
line fails; the rest are still saved.

`--output` is a file or a directory (trailing `/` forces one); `--name` is a file name
stem. Existing files are protected unless `--overwrite`, and the extension follows the
audio actually returned (the CLI never renames or converts bytes).

## Routing and results

Without `--provider`, voxen prefers providers whose declarations cover the request,
then those without a recent failure, then lower `priority`. A channel failure (401,
402, 403, 408, 429, 5xx, a transport error, or a bare `404 page not found`) moves on to
the next provider; an answer about the request itself (an unknown voice, a bad
parameter) is final and never replayed elsewhere. A pinned provider is never replaced.
A dropped connection, 408, 429 or 5xx is first retried twice on the same provider (0.5 s and 2 s apart), per request, so one hiccup does not discard a long multi-chunk job; speech is billed per character, so a repeat costs next to nothing. Then the next provider is tried, each at most once per call. Outcomes are kept in `~/.local/state/voxen/health.json`
(`VOXEN_HOME` or `--home`), keyed by a credential fingerprint so rotation resets them.

`--json` returns the provider, what was requested, and what actually came back, read
from the audio itself: format, sample rate, bit rate, channels, duration and size.
Warnings appear when a provider did not apply something (a different format or sample
rate returned), when the request used something the provider has not declared, or when
the duration is only an estimate (headerless PCM, μ-law or A-law).

## Develop and verify

```sh
go test -race ./...
go vet ./...
go build ./cmd/voxen
```

Tests use local HTTP fixtures and synthetic audio, not paid APIs. They cover request
construction for both kinds, preset and default merging, chunk splitting and ordered
joining (MP3, WAV, Opus), failover and health, pinned providers, declarations versus
warnings, output safety, batch partial failure, configuration validation, and that
credentials never appear in output or state.
