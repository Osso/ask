package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/chzyer/readline"
)

type claudeResult struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
}

func main() {
	rl, err := readline.New("> ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "readline:", err)
		os.Exit(1)
	}
	defer rl.Close()

	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(100),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "glamour:", err)
		os.Exit(1)
	}

	var sessionID string

	for {
		line, err := rl.Readline()
		if err == readline.ErrInterrupt {
			return
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "readline:", err)
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		args := []string{"-p", line, "--output-format", "json", "--dangerously-skip-permissions"}
		if sessionID != "" {
			args = append(args, "--resume", sessionID)
		}

		cmd := exec.Command("claude", args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		stop := startSpinner()
		err = cmd.Run()
		stop()
		if err != nil {
			fmt.Fprintf(os.Stderr, "claude: %v\n%s\n", err, stderr.String())
			continue
		}

		var res claudeResult
		if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
			fmt.Fprintln(os.Stderr, "parse:", err)
			continue
		}

		if res.IsError {
			fmt.Fprintf(os.Stderr, "error: %s\n", res.Result)
			continue
		}

		if res.SessionID != "" {
			sessionID = res.SessionID
		}

		rendered, err := renderer.Render(res.Result)
		if err != nil {
			fmt.Println(res.Result)
			continue
		}
		fmt.Print(rendered)
	}
}

func startSpinner() func() {
	frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		t := time.NewTicker(80 * time.Millisecond)
		defer t.Stop()
		i := 0
		for {
			select {
			case <-done:
				fmt.Fprint(os.Stderr, "\r\033[2K")
				close(stopped)
				return
			case <-t.C:
				fmt.Fprintf(os.Stderr, "\r\033[36m%c\033[0m \033[2mthinking…\033[0m", frames[i%len(frames)])
				i++
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}
