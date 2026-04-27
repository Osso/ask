package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

const cursorBlinkSpeed = 650 * time.Millisecond

// usagePluginDir is the --plugin-dir value we pass to every claude
// subprocess, set once at startup by main() after extracting the
// embedded ask-usage plugin. Empty when extraction failed, in which
// case claudeCLIArgs omits --plugin-dir entirely and the chip just
// goes without 5h/wk segments.
var usagePluginDir string

func applyCursorBlink(ta *textarea.Model, enabled bool) {
	s := ta.Styles()
	s.Cursor.Blink = enabled
	s.Cursor.BlinkSpeed = cursorBlinkSpeed
	ta.SetStyles(s)
}

// applyInputTheme clears the textarea bubble's hardcoded CursorLine background
// (ansi 0 / 255) so the focused row inherits the theme's background instead of
// flashing a dark band across the input.
func applyInputTheme(ta *textarea.Model) {
	s := ta.Styles()
	s.Focused.CursorLine = lipgloss.NewStyle()
	s.Blurred.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(s)
}

func newTab(id int, cfg askConfig) (*model, error) {
	themeName := cfg.UI.Theme
	if themeName == "" {
		themeName = "default"
	}
	applyTheme(themeByName(themeName))

	ta := textarea.New()
	ta.Placeholder = "ask anything (try /resume)"
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.EndOfBufferCharacter = ' '
	ta.CharLimit = 0
	ta.MaxHeight = 0
	ta.DynamicHeight = true
	ta.MinHeight = 3
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "ctrl+j"),
	)
	// Most terminals send ctrl+left/right for word motion; the bubbles
	// default only binds alt+left/right. Add ctrl variants and the
	// matching ctrl+backspace/delete word-deletion keys so editing
	// feels native.
	ta.KeyMap.WordForward = key.NewBinding(
		key.WithKeys("alt+right", "alt+f", "ctrl+right"),
	)
	ta.KeyMap.WordBackward = key.NewBinding(
		key.WithKeys("alt+left", "alt+b", "ctrl+left"),
	)
	ta.KeyMap.DeleteWordBackward = key.NewBinding(
		key.WithKeys("alt+backspace", "ctrl+w", "ctrl+backspace"),
	)
	ta.KeyMap.DeleteWordForward = key.NewBinding(
		key.WithKeys("alt+delete", "alt+d", "ctrl+delete"),
	)
	ta.SetHeight(3)
	ta.Focus()

	cursorBlink := cfg.UI.CursorBlink == nil || *cfg.UI.CursorBlink
	applyCursorBlink(&ta, cursorBlink)
	applyInputTheme(&ta)

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	renderer := newRenderer(100)

	vp := newChatView()
	vp.style = lipgloss.NewStyle().PaddingTop(1)
	vp.mouseWheelEnabled = true

	cwd, _ := os.Getwd()

	provider := providerByID(cfg.Provider)
	if provider == nil {
		return nil, fmt.Errorf("no provider registered")
	}
	settings := provider.LoadSettings()

	// MCP bridge is started unconditionally so hot-swapping the
	// provider in-tab (Ctrl+B) doesn't have to spin up a new listener.
	// Providers that don't consume the bridge (codex) just ignore
	// mcpPort; the cost is a single idle loopback goroutine.
	bridge, err := newMCPBridge(id)
	if err != nil {
		return nil, err
	}
	mcpPort := bridge.port

	m := &model{
		id:                 id,
		cwd:                cwd,
		mcpBridge:          bridge,
		mcpPort:            mcpPort,
		provider:           provider,
		mode:               modeInput,
		input:              ta,
		chat:               vp,
		spinner:            sp,
		renderer:           renderer,
		width:              100,
		height:             30,
		providerSlashCmds:  settings.SlashCommands,
		providerModel:      settings.Model,
		providerEffort:     settings.Effort,
		ollamaHost:         cfg.Claude.Ollama.Host,
		ollamaModel:        cfg.Claude.Ollama.Model,
		themeName:          themeName,
		quietMode:          cfg.UI.QuietMode == nil || *cfg.UI.QuietMode,
		cursorBlink:        cursorBlink,
		renderDiffs:        cfg.UI.RenderDiffs == nil || *cfg.UI.RenderDiffs,
		toolOutputMode:     parseToolOutputMode(cfg.UI.ToolOutput),
		skipAllPermissions: cfg.UI.SkipAllPermissions != nil && *cfg.UI.SkipAllPermissions,
		worktree:           cfg.UI.Worktree != nil && *cfg.UI.Worktree,
		historyIdx:         -1,
		shellOutIdx:        -1,
		shellHistoryIdx:    -1,
		fc:                 &frameCache{},
	}
	m.toast = NewToastModel(40, 3*time.Second)
	m.toast.applyTheme(activeTheme)
	if uc, err := readUsageCache(); err == nil {
		m.usageCache = uc
	}
	m.refreshPrompt()
	return m, nil
}

// printHelp writes the user-facing CLI usage block. Kept as a shared
// helper so `ask --help` (stdout, exit 0) and the `ask resume` arity
// error path (stderr, exit 2) print the exact same text.
func printHelp(w io.Writer) {
	fmt.Fprint(w, `Usage:
  ask                start a new ask TUI session in the current directory
  ask resume <vid>   resume the virtual session with id <vid> — chdirs to
                     the workspace recorded for that session, then opens
                     the TUI already attached to it
  ask --help         show this help

Options:
  --provider <id>                override the provider for this launch
                                 (e.g. "claude", "codex"). Does not modify
                                 the saved config; use /config or /provider
                                 inside the TUI to persist a change.
  --model <name>                 override the provider model for this launch.
                                 Accepts any value the provider's /model
                                 picker would accept (including custom ids).
                                 Does not persist.
  -w, --worktree                 enable worktree mode for this launch. Does
                                 not persist; toggle in /config to save.

Debug:
  --simulate-approval[=<tool>]   open the approval modal at startup with a
                                 synthetic <tool> request (default: Bash).
                                 Exercises the in-app modal UI without
                                 spawning claude.

Virtual session ids look like vs-<hex> and are listed by /resume inside
the TUI. Quitting ask prints the active tab's id so it can be passed to
`+"`"+`ask resume`+"`"+` later.
`)
}

// cliCommand is the post-parse intent for a single ask invocation. main()
// dispatches on Kind: "help" prints usage and exits 0; "resume" runs the
// resume flow with VSID; "run" starts the TUI in the current cwd.
type cliCommand struct {
	Kind string
	VSID string
}

// parseCLICommand validates the post-flag-strip arg vector and rejects
// unknown subcommands or stray flags up-front so users learn about typos
// before the TUI swallows the screen. Pure: no os.Args, no os.Exit, no
// chdir — main() owns side effects.
func parseCLICommand(args []string) (cliCommand, error) {
	if len(args) == 0 {
		return cliCommand{Kind: "run"}, nil
	}
	head := args[0]
	switch head {
	case "--help", "-h", "help":
		if len(args) > 1 {
			return cliCommand{}, fmt.Errorf("unexpected arguments after %q: %v", head, args[1:])
		}
		return cliCommand{Kind: "help"}, nil
	case "resume":
		if len(args) < 2 {
			return cliCommand{}, fmt.Errorf("resume: missing virtual session id")
		}
		if len(args) > 2 {
			return cliCommand{}, fmt.Errorf("resume: unexpected extra arguments: %v", args[2:])
		}
		return cliCommand{Kind: "resume", VSID: args[1]}, nil
	}
	if strings.HasPrefix(head, "-") {
		return cliCommand{}, fmt.Errorf("unknown option: %s", head)
	}
	return cliCommand{}, fmt.Errorf("unknown argument: %s", head)
}

// parseSimulateApprovalFlag scans args for `--simulate-approval` /
// `--simulate-approval=<tool>` and returns whether it was present, the
// tool name (defaulting to "Bash"), and the remaining args with the flag
// stripped. Pure helper so the parsing path is unit-testable.
func parseSimulateApprovalFlag(args []string) (bool, string, []string) {
	enabled := false
	tool := "Bash"
	rest := make([]string, 0, len(args))
	for _, a := range args {
		switch {
		case a == "--simulate-approval":
			enabled = true
		case strings.HasPrefix(a, "--simulate-approval="):
			enabled = true
			if v := strings.TrimPrefix(a, "--simulate-approval="); v != "" {
				tool = v
			}
		default:
			rest = append(rest, a)
		}
	}
	return enabled, tool, rest
}

// parseWorktreeFlag scans args for `-w` / `--worktree` and returns
// whether it was present plus the remaining args with the flag stripped.
// Pure helper so the parsing path is unit-testable. The flag is a
// pure boolean: there is no value form, and no `--no-worktree` opt-out
// — the saved config is the source of truth when the flag is absent.
func parseWorktreeFlag(args []string) (bool, []string) {
	enabled := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "-w", "--worktree":
			enabled = true
		default:
			rest = append(rest, a)
		}
	}
	return enabled, rest
}

// parseProviderModelFlags pulls `--provider <id>` / `--provider=<id>`
// and `--model <name>` / `--model=<name>` out of args, returning the
// extracted values plus the remaining args. Both flags require a
// non-empty value; bare `--provider` or `--model` is an error so a
// typo doesn't silently swallow the next positional argument.
func parseProviderModelFlags(args []string) (provider, model string, rest []string, err error) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--provider":
			if i+1 >= len(args) {
				return "", "", nil, fmt.Errorf("--provider: missing value")
			}
			provider = args[i+1]
			i++
		case strings.HasPrefix(a, "--provider="):
			v := strings.TrimPrefix(a, "--provider=")
			if v == "" {
				return "", "", nil, fmt.Errorf("--provider: missing value")
			}
			provider = v
		case a == "--model":
			if i+1 >= len(args) {
				return "", "", nil, fmt.Errorf("--model: missing value")
			}
			model = args[i+1]
			i++
		case strings.HasPrefix(a, "--model="):
			v := strings.TrimPrefix(a, "--model=")
			if v == "" {
				return "", "", nil, fmt.Errorf("--model: missing value")
			}
			model = v
		default:
			rest = append(rest, a)
		}
	}
	return provider, model, rest, nil
}

// registeredProviderIDs returns the IDs of every provider currently
// registered, in registration order. Used to format the error message
// when --provider names something unknown.
func registeredProviderIDs() []string {
	ids := make([]string, 0, len(providerRegistry))
	for _, p := range providerRegistry {
		ids = append(ids, p.ID())
	}
	return ids
}

// strictProviderByID returns the provider with exactly this id, or nil.
// Unlike providerByID it does NOT fall back to the first registered
// provider, so callers can distinguish "unknown id" from "no providers
// registered".
func strictProviderByID(id string) Provider {
	for _, p := range providerRegistry {
		if p.ID() == id {
			return p
		}
	}
	return nil
}

// injectSimulatedApproval fires a synthetic approvalRequestMsg into the
// running tea.Program so the in-app approval modal opens at startup. The
// reply channel is buffered (size 1) and unread — the modal's send on
// answer is non-blocking, so leaving it dangling is intentional.
func injectSimulatedApproval(p *tea.Program, tabID int, tool string) {
	time.Sleep(150 * time.Millisecond)
	p.Send(approvalRequestMsg{
		tabID:     tabID,
		toolName:  tool,
		input:     simulatedApprovalInput(tool),
		toolUseID: "simulated-approval",
		reply:     make(chan approvalReply, 1),
	})
}

// simulatedApprovalInput returns a tool-shaped input map so the modal's
// summary line has something meaningful to render.
func simulatedApprovalInput(tool string) map[string]any {
	switch tool {
	case "Bash":
		return map[string]any{"command": "ls /tmp"}
	case "Edit", "Write", "MultiEdit", "NotebookEdit", "Read":
		return map[string]any{"file_path": "/tmp/simulated-approval.txt"}
	case "Glob", "Grep":
		return map[string]any{"pattern": "**/*.go"}
	case "WebFetch":
		return map[string]any{"url": "https://example.com"}
	case "WebSearch":
		return map[string]any{"query": "simulated query"}
	default:
		return map[string]any{}
	}
}

// resumeLookup resolves vsID against ~/.config/ask/sessions.json and
// returns the matching VS id, the recorded workspace path, and the
// provider that owned the conversation last (empty for VSes written
// before LastProvider was tracked). Pure: no side effects — main is
// responsible for the os.Chdir, which keeps tests self-contained
// (chdirs from a test process pollute every test that follows because
// the cleanup ordering against t.TempDir teardown is fragile when the
// cwd points inside a doomed tempdir).
func resumeLookup(vsID string) (id, workspace, lastProvider string, err error) {
	if vsID == "" {
		return "", "", "", fmt.Errorf("missing virtual session id")
	}
	store, err := loadVirtualSessions()
	if err != nil {
		return "", "", "", err
	}
	vs := store.findByID(vsID)
	if vs == nil {
		return "", "", "", fmt.Errorf("virtual session %q not found", vsID)
	}
	if vs.Workspace == "" {
		return "", "", "", fmt.Errorf("virtual session %q has no workspace recorded", vsID)
	}
	if _, err := os.Stat(vs.Workspace); err != nil {
		return "", "", "", fmt.Errorf("workspace %s: %w", vs.Workspace, err)
	}
	return vs.ID, vs.Workspace, vs.LastProvider, nil
}

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "_hook" {
		if err := runHookSubcommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "ask _hook:", err)
		}
		return
	}
	simulateApproval, simulateApprovalTool, args := parseSimulateApprovalFlag(os.Args[1:])
	worktreeOverride, args := parseWorktreeFlag(args)
	providerOverride, modelOverride, args, err := parseProviderModelFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ask:", err)
		fmt.Fprintln(os.Stderr)
		printHelp(os.Stderr)
		os.Exit(2)
	}
	if providerOverride != "" && strictProviderByID(providerOverride) == nil {
		fmt.Fprintf(os.Stderr, "ask: unknown provider %q (known: %s)\n",
			providerOverride, strings.Join(registeredProviderIDs(), ", "))
		os.Exit(2)
	}
	cmd, err := parseCLICommand(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ask:", err)
		fmt.Fprintln(os.Stderr)
		printHelp(os.Stderr)
		os.Exit(2)
	}
	var startupResumeVID, resumeProvider string
	switch cmd.Kind {
	case "help":
		printHelp(os.Stdout)
		return
	case "resume":
		vid, ws, lastProv, err := resumeLookup(cmd.VSID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ask resume:", err)
			os.Exit(1)
		}
		if err := os.Chdir(ws); err != nil {
			fmt.Fprintln(os.Stderr, "ask resume: chdir", ws+":", err)
			os.Exit(1)
		}
		startupResumeVID = vid
		resumeProvider = lastProv
	}
	cfg, _ := loadConfig()
	_ = saveConfig(cfg)
	if worktreeOverride {
		t := true
		cfg.UI.Worktree = &t
	}
	if cfg.UI.Worktree != nil && *cfg.UI.Worktree {
		ensureWorktreeGitignore()
	}
	pruneWorktrees()
	if dir, err := extractUsagePlugin(); err != nil {
		debugLog("usage plugin extract: %v", err)
	} else {
		usagePluginDir = dir
	}
	// CLI overrides apply after saveConfig so they don't persist; the
	// next launch should still honour what's on disk unless --provider
	// or --model is passed again. `ask resume` then falls back to the
	// VS's LastProvider so resuming a Claude conversation while the
	// saved default is Codex doesn't reopen with the wrong backend.
	// Explicit --provider always wins; an unknown stored LastProvider
	// is warned about and ignored.
	switch {
	case providerOverride != "":
		cfg.Provider = providerOverride
	case resumeProvider != "":
		if strictProviderByID(resumeProvider) != nil {
			cfg.Provider = resumeProvider
		} else {
			fmt.Fprintf(os.Stderr,
				"ask resume: stored provider %q is no longer registered, falling back to %q\n",
				resumeProvider, cfg.Provider)
		}
	}
	first, err := newTab(1, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ask: mcp:", err)
		os.Exit(1)
	}
	if modelOverride != "" {
		first.providerModel = modelOverride
	}
	if startupResumeVID != "" {
		first.virtualSessionID = startupResumeVID
	}
	a := newApp(first)
	p := tea.NewProgram(a, tea.WithFPS(120))
	setTeaProgram(p)
	if simulateApproval {
		go injectSimulatedApproval(p, first.id, simulateApprovalTool)
	}
	final, err := p.Run()
	if fa, ok := final.(app); ok {
		fa.shutdown()
	}
	pruneWorktrees()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ask:", err)
		os.Exit(1)
	}
}
