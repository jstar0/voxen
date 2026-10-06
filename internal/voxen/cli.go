package voxen

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

const Version = "0.1.0"

// BuildCommit is supplied by the installer; it never changes behavior.
var BuildCommit string

// ExitError carries a process exit code for failures already reported on stdout.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// synthFlags holds the options shared by say and batch.
type synthFlags struct {
	model, provider, preset, voice, lang, format string
	speed                                        float64
	sampleRate, bitRate, latency, chunkChars     int
	normalize                                    bool
	replace                                      []string
	replaceFile                                  string
	output, name                                 string
	overwrite                                    bool
}

func (f *synthFlags) register(cmd *cobra.Command, outputHelp string) {
	fl := cmd.Flags()
	fl.StringVarP(&f.model, "model", "m", "", "Model or alias (default: configured default_model)")
	fl.StringVar(&f.provider, "provider", "", "Pin one configured provider; it is never replaced by another")
	fl.StringVar(&f.preset, "preset", "", "Named preset of voice, language, speed and format from the configuration")
	fl.StringVarP(&f.voice, "voice", "v", "", "Voice ID; see `voxen voices`")
	fl.StringVarP(&f.lang, "lang", "l", "", "Language code, or auto; sent to the provider as given")
	fl.Float64Var(&f.speed, "speed", 0, "Speaking speed multiplier")
	fl.StringVar(&f.format, "format", "", "Audio format, such as mp3, wav, pcm, mulaw or alaw")
	fl.IntVar(&f.sampleRate, "sample-rate", 0, "Sample rate in Hz")
	fl.IntVar(&f.bitRate, "bitrate", 0, "MP3 bit rate in bits per second")
	fl.IntVar(&f.latency, "latency", 0, "Streaming latency optimization level, if the provider supports it")
	fl.BoolVar(&f.normalize, "normalize", false, "Ask the provider to read numbers and abbreviations as spoken words")
	fl.StringArrayVar(&f.replace, "replace", nil, "Pronunciation override WORD=READING (repeatable)")
	fl.StringVar(&f.replaceFile, "replace-file", "", "File of pronunciation overrides: a JSON object or WORD=READING lines")
	fl.IntVar(&f.chunkChars, "chunk-chars", 0, "Split text longer than this many characters into separate requests")
	fl.StringVarP(&f.output, "output", "o", "", outputHelp)
	fl.StringVar(&f.name, "name", "", "File name stem (extension follows the audio actually returned)")
	fl.BoolVar(&f.overwrite, "overwrite", false, "Replace an existing output file")
}

func parseReplace(pairs []string, file string) (map[string]string, error) {
	out := map[string]string{}
	add := func(line string) error {
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			return fmt.Errorf("pronunciation override %q must look like WORD=READING", line)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		return nil
	}
	if file != "" {
		b, err := os.ReadFile(expandPath(file))
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(string(b))
		if strings.HasPrefix(trimmed, "{") {
			var m map[string]string
			if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
				return nil, fmt.Errorf("replace file: %w", err)
			}
			for k, v := range m {
				out[k] = v
			}
		} else {
			for _, line := range strings.Split(trimmed, "\n") {
				if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
					if err := add(line); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	for _, p := range pairs {
		if err := add(p); err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (f *synthFlags) options(cmd *cobra.Command) (SayOptions, error) {
	o := SayOptions{Model: f.model, Provider: f.provider, Preset: f.preset, Voice: f.voice, Language: f.lang, Format: f.format, SampleRate: f.sampleRate, BitRate: f.bitRate, Latency: f.latency, ChunkChars: f.chunkChars, Output: f.output, Name: f.name, Overwrite: f.overwrite}
	if cmd.Flags().Changed("speed") {
		s := f.speed
		o.Speed = &s
	}
	if cmd.Flags().Changed("normalize") {
		n := f.normalize
		o.Normalize = &n
	}
	var err error
	o.Replace, err = parseReplace(f.replace, f.replaceFile)
	return o, err
}

func readText(cmd *cobra.Command, args []string, file string) (string, error) {
	if len(args) > 0 && file != "" {
		return "", fmt.Errorf("give the text as arguments or with --text-file, not both")
	}
	switch {
	case file == "-" || (len(args) == 0 && file == ""):
		if file == "" {
			if f, ok := cmd.InOrStdin().(*os.File); ok {
				if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
					return "", fmt.Errorf("no text given: pass text, --text-file, or pipe it on stdin")
				}
			}
		}
		b, err := io.ReadAll(cmd.InOrStdin())
		return string(b), err
	case file != "":
		b, err := os.ReadFile(expandPath(file))
		return string(b), err
	}
	return strings.Join(args, " "), nil
}

func NewCommand() *cobra.Command {
	var configPath, home string
	var asJSON bool
	root := &cobra.Command{Use: "voxen", Short: "Text-to-speech CLI with configurable providers", Version: Version, SilenceUsage: true, SilenceErrors: true}
	if BuildCommit != "" {
		root.Version += " (" + BuildCommit + ")"
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().StringVar(&configPath, "config", ConfigPath(), "Provider configuration file")
	root.PersistentFlags().StringVar(&home, "home", StateRoot(), "Local state directory (provider health)")
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "Emit stable JSON")

	app := func() (*App, error) {
		cfg, err := LoadConfig(configPath)
		if err != nil {
			return nil, err
		}
		return NewApp(cfg, home), nil
	}
	emit := func(cmd *cobra.Command, v any) error {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	context := func(cmd *cobra.Command) (context.Context, context.CancelFunc) {
		return signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	}

	var sayFlags synthFlags
	var textFile string
	var play, dryRun bool
	say := &cobra.Command{
		Use:   "say [text]",
		Short: "Speak text and save the audio",
		Long:  "Speak text with the configured provider and save the audio. Text comes from arguments, --text-file, or stdin.\nVoices, languages, formats and other values are sent to the provider as given; use `voxen providers --json` to see what each provider declares.",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app()
			if err != nil {
				return err
			}
			o, err := sayFlags.options(cmd)
			if err != nil {
				return err
			}
			if o.Text, err = readText(cmd, args, textFile); err != nil {
				return err
			}
			o.Play, o.DryRun = play, dryRun
			if dryRun {
				plan, err := a.Plan(o)
				if err != nil {
					return err
				}
				return emit(cmd, plan)
			}
			ctx, stop := context(cmd)
			defer stop()
			res, err := a.Say(ctx, o)
			if err != nil {
				if asJSON {
					_ = emit(cmd, map[string]any{"error": err.Error(), "attempts": res.Attempts})
					return &ExitError{Code: 1}
				}
				return err
			}
			if asJSON {
				return emit(cmd, res)
			}
			printResult(cmd.OutOrStdout(), res)
			return nil
		},
	}
	sayFlags.register(say, "Output file, or a directory for an automatic name (default: current directory)")
	say.Flags().StringVar(&textFile, "text-file", "", "Read the text from a file (- for stdin)")
	say.Flags().BoolVar(&play, "play", false, "Play the audio after saving (macOS afplay)")
	say.Flags().BoolVar(&dryRun, "dry-run", false, "Show the plan and request without calling any provider")

	var batchFlags synthFlags
	batch := &cobra.Command{
		Use:   "batch FILE",
		Short: "Synthesize many lines into numbered files",
		Long:  "Read FILE (- for stdin) and synthesize each line into its own file in the output directory. Plain lines are text; lines starting with { are JSON objects with \"text\" and optional name, preset, model, voice, lang, format, speed, normalize and replace overrides. Blank lines and lines starting with # are skipped.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app()
			if err != nil {
				return err
			}
			var in io.Reader = cmd.InOrStdin()
			if args[0] != "-" {
				f, err := os.Open(expandPath(args[0]))
				if err != nil {
					return err
				}
				defer f.Close()
				in = bufio.NewReader(f)
			}
			items, err := ParseBatch(in)
			if err != nil {
				return err
			}
			o, err := batchFlags.options(cmd)
			if err != nil {
				return err
			}
			if o.Output == "" {
				o.Output = "voxen-batch/"
			} else if !strings.HasSuffix(o.Output, "/") {
				o.Output += "/"
			}
			if o.Name != "" {
				return fmt.Errorf("--name does not apply to batch; give a \"name\" in individual lines")
			}
			ctx, stop := context(cmd)
			defer stop()
			results := a.Batch(ctx, items, o)
			failed := 0
			for _, r := range results {
				if r.Error != "" {
					failed++
				}
			}
			if asJSON {
				if err := emit(cmd, results); err != nil {
					return err
				}
			} else {
				for _, r := range results {
					if r.Error != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "%3d  FAILED  %s\n", r.Index, r.Error)
						continue
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%3d  ", r.Index)
					printResult(cmd.OutOrStdout(), *r.Result)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%d of %d succeeded\n", len(results)-failed, len(results))
			}
			if failed > 0 {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}
	batchFlags.register(batch, "Output directory (default: ./voxen-batch)")

	var vModel, vProvider, vGender string
	voices := &cobra.Command{
		Use:   "voices",
		Short: "List the voices a provider offers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app()
			if err != nil {
				return err
			}
			ctx, stop := context(cmd)
			defer stop()
			res, err := a.Voices(ctx, vModel, vProvider, vGender)
			if err != nil {
				return err
			}
			if asJSON {
				return emit(cmd, res)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			for _, v := range res.Voices {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", v.ID, v.Gender, v.Language, v.Name)
			}
			w.Flush()
			fmt.Fprintf(cmd.OutOrStdout(), "%d voices (%s, provider %s)\n", len(res.Voices), res.Source, res.Provider)
			return nil
		},
	}
	voices.Flags().StringVarP(&vModel, "model", "m", "", "Model or alias")
	voices.Flags().StringVar(&vProvider, "provider", "", "Ask this provider")
	voices.Flags().StringVar(&vGender, "gender", "", "Only voices of this gender")

	models := &cobra.Command{Use: "models", Short: "List models, aliases and the providers serving them", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app()
		if err != nil {
			return err
		}
		views := a.Models()
		if asJSON {
			return emit(cmd, views)
		}
		for _, v := range views {
			line := v.Model
			if v.Default {
				line += " (default)"
			}
			if len(v.Aliases) > 0 {
				line += "  aliases: " + strings.Join(v.Aliases, ", ")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s  providers: %s\n", line, strings.Join(v.Providers, ", "))
		}
		return nil
	}}

	providers := &cobra.Command{Use: "providers", Short: "Show providers, what they declare they support, and recent health", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app()
		if err != nil {
			return err
		}
		views := a.Providers()
		if asJSON {
			return emit(cmd, views)
		}
		out := cmd.OutOrStdout()
		for _, v := range views {
			fmt.Fprintf(out, "%s  %s  %s\n", v.Name, v.Kind, v.BaseURL)
			fmt.Fprintf(out, "  models: %s\n", strings.Join(v.Models, ", "))
			fmt.Fprintf(out, "  capabilities: %s\n", strings.Join(v.Capabilities, ", "))
			if len(v.Formats) > 0 {
				fmt.Fprintf(out, "  formats: %s\n", strings.Join(v.Formats, ", "))
			}
			if len(v.Languages) > 0 {
				fmt.Fprintf(out, "  languages: %s\n", strings.Join(v.Languages, ", "))
			}
			if v.Voices != "" {
				fmt.Fprintf(out, "  voices: %s\n", v.Voices)
			}
			fmt.Fprintf(out, "  credential: %s\n", v.Credential)
			if v.Health != nil && !v.Health.LastSuccess.IsZero() {
				fmt.Fprintf(out, "  last success: %s\n", v.Health.LastSuccess.Local().Format(time.DateTime))
			}
			if v.Degraded {
				fmt.Fprintf(out, "  degraded: %s\n", v.Health.LastError)
			}
			for _, n := range v.Notes {
				fmt.Fprintf(out, "  note: %s\n", n)
			}
		}
		return nil
	}}

	root.AddCommand(say, batch, voices, models, providers)
	return root
}

func printResult(w io.Writer, r Result) {
	detail := r.Format
	if r.SampleRate > 0 {
		detail += fmt.Sprintf(" %dHz", r.SampleRate)
	}
	if r.BitRate > 0 && r.Format == "mp3" {
		detail += fmt.Sprintf(" %dkbps", r.BitRate/1000)
	}
	approx := ""
	if r.DurationAssumed {
		approx = "~"
	}
	fmt.Fprintf(w, "%s  %s  %s%.1fs  %s", filepath.Clean(r.Path), detail, approx, r.DurationSeconds, "via "+r.Provider)
	if r.Chunks > 1 {
		fmt.Fprintf(w, "  (%d chunks)", r.Chunks)
	}
	fmt.Fprintln(w)
	for _, warning := range r.Warnings {
		fmt.Fprintln(w, "Warning:", warning)
	}
}
