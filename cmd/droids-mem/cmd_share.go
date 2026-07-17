package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/samuelmolero26/droids-mem/internal/share"
	"github.com/samuelmolero26/droids-mem/internal/state"
	"github.com/samuelmolero26/droids-mem/internal/store"
)

// usePersistedRepo is the NoOptDefVal for --repo: `--repo` with no value means
// "reuse the persisted Memory repo", distinct from `--repo <path>` (adopt) and
// an absent flag (bare stdio export/import).
const usePersistedRepo = "\x00use-persisted"

// flagUsage is printed when `share` runs without a TTY (FR-1): agents/CI never
// get a prompt, they get the declarative flag surface instead.
const flagUsage = `share is interactive and needs a terminal. For agents/scripts use the flags:
  droids-mem export --repo <path>   # Publish shared memories to a Memory repo
  droids-mem export --repo          # Publish to the persisted Memory repo
  droids-mem import --repo <path>   # Fetch a Memory repo into the local store
  droids-mem import --repo          # Fetch from the persisted Memory repo`

// newShareCmd is dual-mode (FR-1). `share --id <id>` flips one memory to
// scope='shared' (the ADR-0028 grant). Bare `share` on a TTY runs the guided
// Publish flow; off a TTY it prints the flag usage and exits.
func newShareCmd(a *app) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:     "share",
		Short:   "Share a memory (--id) or run the guided Publish flow (no args)",
		Example: "  droids-mem share --id mem_01J9KXVR2E...\n  droids-mem share",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			if id != "" {
				return setScope(cmd, s, "share", id, "shared")
			}
			return runShareFlow(cmd, s)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "Memory ID to mark shared (mem_ prefix); omit to run the guided flow")
	return cmd
}

// newUnshareCmd reverts a memory to scope='personal', pulling it back out of
// the exportable pool.
func newUnshareCmd(a *app) *cobra.Command {
	return newSetScopeCmd(a, "unshare", "personal", "Mark a memory as personal (excluded from export)")
}

func newSetScopeCmd(a *app, use, scope, short string) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:     use,
		Short:   short,
		Example: "  droids-mem " + use + " --id mem_01J9KXVR2E...",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			return setScope(cmd, s, use, id, scope)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "Memory ID with mem_ prefix (required)")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

// setScope flips one memory's scope, emitting the standard ok/not-found output.
func setScope(cmd *cobra.Command, s *store.Store, use, id, scope string) error {
	found, err := s.SetScope(cmd.Context(), id, scope)
	if err != nil {
		writeError(use+"_failed", err.Error(), true)
		exitWith(ExitError)
	}
	if !found {
		writeError("not_found", "no memory with id "+id, false,
			withField("id"),
			withSuggestion("use 'droids-mem list' to find valid IDs"),
		)
		exitWith(ExitNotFound)
	}
	writeJSON(map[string]string{"status": "ok", "id": id, "scope": scope})
	return nil
}

// newExportCmd streams shared memories to stdout, or Publishes them to a Memory
// repo with --repo (FR-2).
func newExportCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "export",
		Short:   "Export shared memories as JSONL to stdout, or --repo to Publish",
		Example: "  droids-mem export > team/shared.jsonl\n  droids-mem export --repo ~/mem-repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("repo") {
				repo, err := resolveRepo(repoFlagValue(cmd, args))
				if err != nil {
					writeError("repo_unresolved", err.Error(), false)
					exitWith(ExitError)
				}
				res, err := share.Publish(cmd.Context(), s, repo)
				if err != nil {
					handleTransportErr("publish_failed", err)
				}
				writeJSON(res)
				return nil
			}
			if err := s.ExportShared(cmd.Context(), cmd.OutOrStdout()); err != nil {
				writeError("export_failed", err.Error(), true)
				exitWith(ExitError)
			}
			return nil
		},
	}
	addRepoFlag(cmd, "Publish to the Memory repo (path adopts+persists; bare reuses the persisted repo)")
	return cmd
}

// newImportCmd reads a shared-pool JSONL stream from stdin, or Fetches from a
// Memory repo with --repo (FR-2).
func newImportCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "import",
		Short:   "Import a shared-pool JSONL stream from stdin, or --repo to Fetch",
		Example: "  droids-mem import < team/shared.jsonl\n  droids-mem import --repo ~/mem-repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("repo") {
				repo, err := resolveRepo(repoFlagValue(cmd, args))
				if err != nil {
					writeError("repo_unresolved", err.Error(), false)
					exitWith(ExitError)
				}
				res, err := share.Fetch(cmd.Context(), s, repo)
				if err != nil {
					handleTransportErr("import_failed", err)
				}
				writeJSON(res)
				return nil
			}
			res, err := s.ImportShared(cmd.Context(), cmd.InOrStdin())
			if err != nil {
				writeError("import_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(res)
			return nil
		},
	}
	addRepoFlag(cmd, "Fetch from the Memory repo (path adopts+persists; bare reuses the persisted repo)")
	return cmd
}

// addRepoFlag wires --repo so it can appear bare (reuse persisted) or with a path.
func addRepoFlag(cmd *cobra.Command, usage string) {
	cmd.Flags().String("repo", "", usage)
	cmd.Flags().Lookup("repo").NoOptDefVal = usePersistedRepo
}

// repoFlagValue extracts the --repo value. NoOptDefVal makes `--repo <path>`
// (space-separated) land the path in positional args, not the flag, so fall
// back to the first positional; `--repo=<path>` and bare `--repo` still work.
func repoFlagValue(cmd *cobra.Command, args []string) string {
	val := cmd.Flags().Lookup("repo").Value.String()
	if val == usePersistedRepo && len(args) > 0 {
		return args[0]
	}
	return val
}

// resolveRepo turns a --repo flag value into a usable Memory repo path.
// usePersistedRepo → the persisted repo (error with a pointer if none); any
// other value → adopt that path (must be an existing git repo) and persist it.
func resolveRepo(flagVal string) (string, error) {
	if flagVal == usePersistedRepo {
		repo, err := state.ShareRepoPath()
		if err != nil {
			return "", err
		}
		if repo == "" {
			return "", errors.New("no Memory repo configured; run 'droids-mem share' or pass --repo <path>")
		}
		return repo, nil
	}
	return adoptRepo(flagVal)
}

// adoptRepo validates a path as an existing git repo (FR-4: non-git → hard
// error, since a Publish would git-add foreign files), then persists it as the
// current repo and records it in the tracked set.
func adoptRepo(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("empty --repo path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if !share.IsGitRepo(abs) {
		return "", fmt.Errorf("%s is not a git repo; create or clone the Memory repo first", abs)
	}
	if err := state.SetShareRepo(abs, share.RemoteURL(abs)); err != nil {
		return "", err
	}
	return abs, nil
}

// handleTransportErr maps a git-transport failure to the CLI error envelope,
// preserving the retryable flag (a non-fast-forward push re-run succeeds; FR-5).
func handleTransportErr(code string, err error) {
	var re *share.RetryableError
	if errors.As(err, &re) {
		writeError(code, err.Error(), true,
			withSuggestion("re-run the command; Publish is fetch-first and idempotent"))
		exitWith(ExitError)
	}
	writeError(code, err.Error(), false)
	exitWith(ExitError)
}

// runShareFlow is the TTY-gated guided Publish flow (FR-1). Off a terminal it
// prints the flag usage and exits — an agent/CI never blocks on a prompt.
func runShareFlow(cmd *cobra.Command, s *store.Store) error {
	if !stdinIsTTY() {
		writeString(flagUsage)
		return nil
	}
	in := bufio.NewReader(os.Stdin)
	repo, err := pickRepo(cmd, in)
	if err != nil {
		writeError("repo_unresolved", err.Error(), false)
		exitWith(ExitError)
	}
	if err := pickMemories(cmd, s, in); err != nil {
		writeError("share_failed", err.Error(), true)
		exitWith(ExitError)
	}
	count, err := s.CountShared(cmd.Context())
	if err != nil {
		writeError("share_failed", err.Error(), true)
		exitWith(ExitError)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s %s %s %s ",
		paint("Publish", cBold, cMint),
		paint(fmt.Sprintf("%d shared memories", count), cBright),
		paint("to "+repo+"?", cTaupe),
		paint("[y/N]", cDim))
	if !yes(readLine(in)) {
		fmt.Fprintln(out, paint("aborted", cPink))
		return nil
	}
	res, err := share.Publish(cmd.Context(), s, repo)
	if err != nil {
		handleTransportErr("publish_failed", err)
	}
	writeJSON(res)
	return nil
}

// pickRepo presents the tracked repos plus "enter a path" / "create a new one"
// (FR-1 step 1). Stdlib numbered prompt — no interactive-prompt dependency (N8).
func pickRepo(cmd *cobra.Command, in *bufio.Reader) (string, error) {
	known, _ := state.KnownRepos()
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, paint("Select a Memory repo:", cBold, cMint))
	for i, r := range known {
		label := paint(r.Path, cBright)
		if i == 0 {
			label += paint("  (last used)", cDim)
		}
		fmt.Fprintf(out, "  %s %s\n", paint(strconv.Itoa(i+1)+")", cDim), label)
	}
	enterIdx, createIdx := len(known)+1, len(known)+2
	fmt.Fprintf(out, "  %s %s\n", paint(strconv.Itoa(enterIdx)+")", cDim), "enter a path")
	fmt.Fprintf(out, "  %s %s\n", paint(strconv.Itoa(createIdx)+")", cDim), "create a new one")
	fmt.Fprint(out, paint("Choice: ", cTaupe))
	choice, _ := strconv.Atoi(strings.TrimSpace(readLine(in)))
	switch {
	case choice >= 1 && choice <= len(known):
		return adoptRepo(known[choice-1].Path)
	case choice == enterIdx:
		fmt.Fprint(out, "Path to an existing git Memory repo: ")
		return adoptRepo(readLine(in))
	case choice == createIdx:
		return createRepo(cmd, in)
	default:
		return "", fmt.Errorf("invalid choice")
	}
}

// pickMemories shows the newest memories and flips the chosen ones to
// scope='shared' before the Publish confirm — so a human shares "what I just
// did" without hunting ULIDs for `share --id`. Empty input skips (Publish still
// covers memories already shared). Same stdlib numbered prompt as pickRepo (N8).
func pickMemories(cmd *cobra.Command, s *store.Store, in *bufio.Reader) error {
	res, err := s.List(cmd.Context(), store.ListRequest{Limit: 20})
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(res.Memories) == 0 {
		return nil
	}
	fmt.Fprintln(out, paint("\nShare which memories?", cBold, cMint)+paint(" (comma list, 'all', enter to skip)", cDim))
	for i, m := range res.Memories {
		scope := m.Scope
		if scope == "" {
			scope = "personal"
		}
		tag := paint("[personal]", cDim)
		if scope == "shared" {
			tag = paint("[shared]  ", cMint)
		}
		fmt.Fprintf(out, "  %s %s %s %s\n",
			paint(strconv.Itoa(i+1)+")", cDim), tag,
			paint(fmt.Sprintf("%-13s", m.Kind), cTaupe), paint(m.Title, cBright))
	}
	fmt.Fprint(out, paint("Choice: ", cTaupe))
	picks := parsePicks(readLine(in), len(res.Memories))
	marked := 0
	for _, idx := range picks {
		m := res.Memories[idx]
		if m.Scope == "shared" {
			continue // already in the pool — idempotent skip
		}
		if _, err := s.SetScope(cmd.Context(), m.ID, "shared"); err != nil {
			return err
		}
		marked++
	}
	if marked > 0 {
		fmt.Fprintln(out, paint(fmt.Sprintf("→ %d marked shared.", marked), cMint))
	}
	return nil
}

// parsePicks turns "all" or a comma list of 1-based positions into 0-based
// indices into a slice of length n. Out-of-range and non-numeric entries are
// dropped; empty input yields no picks.
func parsePicks(line string, n int) []int {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if strings.EqualFold(line, "all") {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		return idx
	}
	var picks []int
	for tok := range strings.SplitSeq(line, ",") {
		if v, err := strconv.Atoi(strings.TrimSpace(tok)); err == nil && v >= 1 && v <= n {
			picks = append(picks, v-1)
		}
	}
	return picks
}

// createRepo runs the gh-backed create step (FR-4, human-only). Absent gh → a
// pointer to do it by hand and re-run with --repo. droids-mem never hand-rolls
// git-host logic.
func createRepo(cmd *cobra.Command, in *bufio.Reader) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", errors.New("gh not found; create the repo and re-run, or pass --repo <clone-path>")
	}
	out := cmd.OutOrStdout()
	fmt.Fprint(out, paint("New repo name: ", cTaupe))
	name := strings.TrimSpace(readLine(in))
	if name == "" {
		return "", errors.New("repo name required")
	}
	home, _ := os.UserHomeDir()
	def := filepath.Join(home, name)
	fmt.Fprintf(out, "%s%s%s ", paint("Clone to [", cTaupe), paint(def, cBright), paint("]:", cTaupe))
	dest := strings.TrimSpace(readLine(in))
	if dest == "" {
		dest = def
	} else if abs, err := filepath.Abs(dest); err == nil {
		dest = abs
	}
	if err := runGH(cmd, "repo", "create", name, "--private"); err != nil {
		return "", err
	}
	if err := runGH(cmd, "repo", "clone", name, dest); err != nil {
		return "", err
	}
	return adoptRepo(dest)
}

func runGH(cmd *cobra.Command, args ...string) error {
	// #nosec G204 -- fixed "gh" binary; args are the fixed create/clone subcommand
	// plus a repo name the human just typed at the interactive prompt (FR-4).
	c := exec.Command("gh", args...)
	c.Stdout, c.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := c.Run(); err != nil {
		return fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// stdinIsTTY reports whether stdin is an interactive terminal (stdlib only, no
// x/term dependency — PRD §7). A pipe or file is not a char device, so the
// realistic agent/CI path (piped stdin) is correctly detected as non-interactive.
// ponytail: this can't tell a tty from /dev/null (both are char devices); a
// human `share < /dev/null` would enter the prompt and hit EOF. Swap in an
// ioctl-based check (x/sys/unix, already in the module graph) if that bites.
func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func readLine(in *bufio.Reader) string {
	line, _ := in.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

func yes(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
