package main

import (
	"os/exec"
	"sync"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	glamour "charm.land/glamour/v2"
)

type slashCmd struct {
	name string
	desc string
}

type sessionEntry struct {
	id      string
	cwd     string
	preview string
	modTime time.Time
}

type viewMode int

const (
	modeInput viewMode = iota
	modeSessionPicker
	modeAskQuestion
	modeApproval
	modeConfig
)

type streamStatusMsg struct {
	status string
	proc   *providerProc
}

type assistantTextMsg struct {
	text string
	proc *providerProc
}

type turnCompleteMsg struct {
	proc *providerProc
}

type todoItem struct {
	Content    string
	ActiveForm string
	Status     string
}

type todoUpdatedMsg struct {
	todos []todoItem
	proc  *providerProc
}

type bgTaskStartedMsg struct {
	taskID string
	proc   *providerProc
}

type bgTaskEndedMsg struct {
	taskID string
	proc   *providerProc
}

type diffHunk struct {
	oldStart int
	oldLines int
	newStart int
	newLines int
	lines    []string
}

type toolDiffMsg struct {
	filePath string
	hunks    []diffHunk
	proc     *providerProc
}

type stderrBuf struct {
	mu   sync.Mutex
	data []byte
}

func (s *stderrBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = append(s.data, p...)
	if len(s.data) > 8192 {
		s.data = s.data[len(s.data)-8192:]
	}
	return len(p), nil
}

func (s *stderrBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.data)
}

type historyKind int

const (
	histPrerendered historyKind = iota
	histResponse
	histUser
)

type historyEntry struct {
	kind     historyKind
	text     string
	rendered string
}

type sessionsLoadedMsg struct {
	sessions []sessionEntry
	err      error
}

type historyLoadedMsg struct {
	sessionID string
	entries   []historyEntry
	err       error
	silent    bool
}

type frameCache struct {
	vpFP   string
	vpView string

	vbFP      string
	vbWithBar string
}

type closeTabMsg struct {
	tabID int
}

type model struct {
	id        int
	cwd       string
	mcpBridge *mcpBridge

	provider Provider

	input     textarea.Model
	viewport  viewport.Model
	spinner   spinner.Model
	renderer  *glamour.TermRenderer
	sessionID string
	resumeCwd string
	busy      bool
	width     int
	height    int

	history []historyEntry

	mode      viewMode
	menuIdx   int
	sessions  []sessionEntry
	pickerIdx int

	pathMatches []string
	pathIdx     int

	status   string
	streamCh chan tea.Msg
	proc     *providerProc

	pending     []pendingAttachment
	nextImageID uint32

	scrollbarDragging bool

	askQuestions        []question
	askAnswers          []qAnswer
	askTab              int
	askCursor           int
	askEditing          askEditField
	askNoteBackup       string
	askReply            chan askReply
	askMode             askMode
	askConfirmingCancel bool
	askCancelChoice     int

	askOllamaActive bool
	askOllamaField  int
	askOllamaHost   string
	askOllamaModel  string
	askOllamaErr    string

	approvalTool   string
	approvalInput  map[string]any
	approvalReply  chan approvalReply
	approvalChoice int

	cancelTurnConfirming bool
	cancelTurnChoice     int

	closeTabConfirming bool
	closeTabChoice     int

	shellMode         bool
	shellBsArmed      bool
	shellCh           chan tea.Msg
	shellProc         *exec.Cmd
	shellOutIdx       int
	shellHistory      []string
	shellHistoryIdx   int
	shellHistoryDraft string

	configFilter string
	configCursor int

	configThemePickerActive bool
	configThemeCursor       int
	configThemeBackup       string

	themeName string

	quietMode          bool
	cursorBlink        bool
	renderDiffs        bool
	skipAllPermissions bool
	worktree           bool
	worktreeName       string
	turnBuffer         []string

	lastContentFP string

	fc *frameCache

	mcpPort           int
	providerModel     string
	providerEffort    string
	ollamaHost        string
	ollamaModel       string
	providerSlashCmds []providerSlashEntry

	inputHistory []string
	historyIdx   int
	historyDraft string

	exitArmed bool

	todos []todoItem

	bgTasks map[string]struct{}
}

type askMode int

const (
	askForMCP askMode = iota
	askForModel
	askForEffort
)

type pendingAttachment struct {
	data      []byte
	mime      string
	imageID   uint32
	thumbCols int
	thumbRows int
}

const (
	pathBoxHeight   = 10
	pathBoxMinWidth = 32
	boxChromeW      = 4 // rounded border (2) + horizontal padding (2)
)
