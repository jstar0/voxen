# Adding a provider

Add an entry under `providers` in the configuration; the skill and the CLI need no
change, because clients read the entry through `voxen providers --json`.

1. Choose `kind` by protocol: `xai-tts` (`/tts`) or `openai-speech` (`/audio/speech`).
2. Reference a credential (`api_key_env`, `api_key_file` or `env_file`).
3. List the exact `supported_models`.
4. Declare only what you have seen work: `capabilities`, and where known `formats`,
   `sample_rates`, `bit_rates`, `languages`, `tags`, `voices`, `limits`.
5. Put surprises in `notes` (parameters a gateway ignores, models it does not
   distinguish). Verify with real calls: `voxen say … --provider NAME --json` reports
   what actually came back and warns where it differs from what was asked.

Giving two providers the same model makes them failover candidates for each other;
`priority` orders them.
