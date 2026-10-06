---
name: voxen
description: Convert text to speech with the voxen CLI: choose voice, language, speed and audio format, add speech tags and pronunciation overrides, speak long text or many lines at once, and save audio files. Providers and their abilities come from local configuration, so check what is available before assuming anything.
---

# Voxen

Use `voxen` (fallback `~/.local/bin/voxen`) to turn text into audio files. Credentials and channels are configured locally; the CLI loads them. What a channel can do is decided by the channel, not by this skill, so look it up instead of assuming.

## Find out what is available

```bash
voxen providers --json   # each provider: models, capabilities, formats, sample_rates, bit_rates, languages, tags, voices, limits, notes, credential and recent health
voxen models --json      # models, aliases, the default, and which providers serve each
voxen voices --json      # voices of the provider that would serve the request; add --provider, --model, --gender
voxen say --help         # every option
```

Read `providers --json` before using an option you have not used with that provider: `capabilities`, `formats`, `languages`, `tags` and `limits` are what the channel's owner declared, and `notes` records known quirks (parameters a channel ignores, models it does not distinguish). An empty list means the provider makes no claim. Declarations steer routing and produce warnings; they never block a request, so the real answer is the response.

## Speak

```bash
voxen say "text" --voice VOICE --lang CODE --output ./audio/ --name intro --json
voxen say --text-file ./script.txt --preset NAME -o ./audio/
echo "text" | voxen say -o ./audio/
```

- Text comes from arguments, `--text-file` (`-` for stdin) or standard input.
- `--voice`, `--lang`, `--speed`, `--format`, `--sample-rate`, `--bitrate`, `--latency`, `--normalize` and `--model` are passed to the provider exactly as given. Take voice names from `voxen voices` and language codes from the provider's `languages`; do not guess.
- `--normalize` asks the provider to read numbers, dates and abbreviations as spoken words. `--replace WORD=READING` (repeatable) and `--replace-file` override pronunciation of specific words.
- Speech tags such as pauses, laughter, whispering or slow speech are written inline in the text. Use only the forms listed in the provider's `tags` and place them exactly as listed.
- `--preset NAME` applies a named bundle (voice, language, speed, format and more) from the configuration; explicit flags override it. Aliases and defaults are also configured locally.
- `--output` is a file or a directory (a trailing `/` forces a directory); `--name` sets the file name stem. Existing files are never replaced without `--overwrite`. The extension follows the audio actually returned.
- Leave `--provider` unset unless the user names a channel. Without it, a provider whose declarations cover the request is preferred, and transient failures are retried and then a channel failure moves on to the next provider; a pinned provider is never replaced.
- `--dry-run` shows the plan and the request body without contacting any provider. `--play` plays the file on macOS after saving.

## Long text and many lines

Text longer than the provider's `chunk_chars` limit is split at sentence boundaries, synthesized in separate requests and joined into one file; speech tags are kept intact across the split. `chunks` in the JSON says how many requests were used. Long text can take minutes.

```bash
voxen batch lines.txt -o ./audio/ --voice VOICE --json
```

Each line of the file is one output, numbered `001`, `002`, and so on. A line starting with `{` is a JSON object with `text` and optional `name`, `preset`, `model`, `voice`, `lang`, `format`, `speed`, `normalize` and `replace`, so different lines can use different voices. Blank lines and `#` comments are skipped. The exit code is 1 if any line failed; the others are still saved.

## Deliver

Use `--json` and read it rather than trusting exit code 0. Report each file's `path`, the real `format`, `sample_rate`, `bit_rate` and `duration_seconds` (`duration_assumed` means the duration was derived from the request because the audio has no header), and the `provider` that answered. Report every entry in `warnings`: it means the provider did not apply something that was requested (for example a different format or sample rate came back), or that the request used something the provider has not declared. If the first provider failed and another answered, `attempts` shows it. Errors carry the provider's own message; relay it. A different voice or language is a decision for the user, not a silent fallback.

Never print or store API keys. Voice cloning, live streaming and per-character timestamps are not provided by this tool; do not promise them even if an upstream service advertises them.
