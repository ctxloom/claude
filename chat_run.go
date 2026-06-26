package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/ctxloom/shared/agent"
)

// This file implements the StructuredChat capability for claude-code over its
// `--input-format stream-json` mode: user messages are written to the process's
// stdin as NDJSON, and the NDJSON event stream from stdout is normalized (see
// chat_stream.go) onto the outbound channel. No pty, no keystroke injection.

// chatTransport is the I/O seam for a stream-json conversation: a writable stdin,
// a readable stdout, and a teardown. Default = a spawned `claude` process; tests
// inject in-memory pipes so they never spawn anything.
type chatTransport struct {
	stdin  io.WriteCloser
	stdout io.Reader
	close  func() error
}

// Close tears the transport down (and unblocks a reader parked on stdout).
func (t *chatTransport) Close() error {
	if t.close != nil {
		return t.close()
	}
	return nil
}

type chatTransportFunc func(ctx context.Context, args []string, env map[string]string, workDir string) (*chatTransport, error)

// Chat runs a structured conversation. It honors the agent.StructuredChat
// contract: the caller closes `in` to end input; this closes `out` exactly once
// before returning; it returns when input is closed and the final response
// drains, when ctx is cancelled, or on a fatal error.
func (b *ClaudeCode) Chat(parentCtx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	open := b.openChatTransport
	if open == nil {
		open = b.spawnChatTransport
	}
	tr, err := open(ctx, b.chatArgs(req), req.Env, req.WorkDir)
	if err != nil {
		return err
	}

	// Reader: stdout NDJSON → normalized events → out. Closes readerDone on exit.
	readerDone := make(chan struct{})
	go readChatEvents(ctx, tr.stdout, out, readerDone, b.clock())

	// teardown closes the transport (unblocking a parked reader) then waits for
	// the reader to finish, so no send races the deferred close(out).
	teardown := func() {
		_ = tr.Close()
		<-readerDone
	}

	for {
		select {
		case <-ctx.Done():
			teardown()
			return ctx.Err()
		case msg, ok := <-in:
			if !ok {
				// No more input: half-close stdin so claude completes the last
				// turn and exits, draining stdout to EOF; then tear down.
				_ = tr.stdin.Close()
				<-readerDone
				_ = tr.Close()
				return nil
			}
			if err := writeUserMessage(tr.stdin, msg.Text); err != nil {
				teardown()
				return err
			}
		}
	}
}

// clock returns the timestamp source for chat entries, defaulting to time.Now
// when none was injected.
func (b *ClaudeCode) clock() func() time.Time {
	if b.now != nil {
		return b.now
	}
	return time.Now
}

// readChatEvents reads newline-delimited JSON from stdout (no line-length cap —
// tool outputs can be large) and maps each line to ChatEvents on out, stopping
// on EOF/error or ctx cancellation. Each entry is stamped with a receipt time
// (see stampEntryTime) since stream-json carries no per-event timestamp.
func readChatEvents(ctx context.Context, stdout io.Reader, out chan<- agent.ChatEvent, done chan<- struct{}, now func() time.Time) {
	defer close(done)
	br := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			for _, ev := range mapStreamJSONEvent(line) {
				select {
				case out <- stampEntryTime(ev, now):
				case <-ctx.Done():
					return
				}
			}
		}
		if err != nil {
			return // EOF or read error (e.g. transport closed)
		}
	}
}

// stampEntryTime records receipt time on an entry event that arrived without a
// timestamp. claude-code's stream-json carries no per-event time, so the live
// chat stream always lands here; a transcript-derived entry already has one and
// is left untouched. Clock-injected so the fallback is deterministic in tests.
func stampEntryTime(ev agent.ChatEvent, now func() time.Time) agent.ChatEvent {
	if ev.Entry != nil && ev.Entry.Timestamp.IsZero() {
		ev.Entry.Timestamp = now()
	}
	return ev
}

// sjUserOut is the NDJSON user message written to stdin, matching claude's
// stream-json input schema: {"type":"user","message":{"role":"user","content":...}}.
type sjUserOut struct {
	Type    string `json:"type"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
}

func writeUserMessage(w io.Writer, text string) error {
	var m sjUserOut
	m.Type = "user"
	m.Message.Role = "user"
	m.Message.Content = text
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// chatArgs builds the stream-json command line: base args + the print/streaming
// flags, plus model and permission bypass when requested.
func (b *ClaudeCode) chatArgs(req agent.ChatRequest) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)
	args = append(args, "-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
	)
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.AutoApprove {
		args = append(args, "--dangerously-skip-permissions")
	}
	return args
}

// spawnChatTransport launches the real `claude` process with piped stdio (NOT a
// pty). stderr passes through for diagnostics.
func (b *ClaudeCode) spawnChatTransport(ctx context.Context, args []string, env map[string]string, workDir string) (*chatTransport, error) {
	cmd := exec.CommandContext(ctx, b.BinaryPath, args...)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &chatTransport{
		stdin:  stdin,
		stdout: stdout,
		close: func() error {
			_ = stdin.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			return cmd.Wait()
		},
	}, nil
}
