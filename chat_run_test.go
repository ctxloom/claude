package claude

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// ClaudeCode must satisfy the optional StructuredChat capability.
var _ agent.StructuredChat = (*ClaudeCode)(nil)

func TestChatArgs_StreamJSONFlags(t *testing.T) {
	b := &ClaudeCode{}
	joined := strings.Join(b.chatArgs(agent.ChatRequest{Model: "sonnet", AutoApprove: true}), " ")
	assert.Contains(t, joined, "-p")
	assert.Contains(t, joined, "--input-format stream-json")
	assert.Contains(t, joined, "--output-format stream-json")
	assert.Contains(t, joined, "--verbose")
	assert.Contains(t, joined, "--model sonnet")
	assert.Contains(t, joined, "--dangerously-skip-permissions")
}

// TestChat_PumpsMessagesAndStreamsEvents: a user message is written to the
// transport's stdin as one NDJSON line, and the transport's stdout NDJSON is
// mapped to ChatEvents on `out`; `out` is closed on return.
func TestChat_PumpsMessagesAndStreamsEvents(t *testing.T) {
	stdout := strings.NewReader(
		`{"type":"system","subtype":"init","model":"m","mcp_servers":[]}` + "\n" +
			`{"type":"assistant","message":{"content":[{"type":"text","text":"hi there"}]}}` + "\n" +
			`{"type":"result","subtype":"success","usage":{"input_tokens":10},"modelUsage":{"m":{"contextWindow":1000,"outputTokens":3}},"total_cost_usd":0.01}` + "\n")
	var stdin bytes.Buffer

	b := &ClaudeCode{}
	b.openChatTransport = func(_ context.Context, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		return &chatTransport{stdin: nopWriteCloser{&stdin}, stdout: stdout, close: func() error { return nil }}, nil
	}

	in := make(chan agent.ChatMessage, 1)
	out := make(chan agent.ChatEvent)
	in <- agent.ChatMessage{Text: "hello"}
	close(in)

	var evs []agent.ChatEvent
	collected := make(chan struct{})
	go func() {
		for ev := range out {
			evs = append(evs, ev)
		}
		close(collected)
	}()

	require.NoError(t, b.Chat(context.Background(), agent.ChatRequest{Model: "m"}, in, out))
	<-collected

	// stdin received the NDJSON user message.
	assert.Contains(t, stdin.String(), `"type":"user"`)
	assert.Contains(t, stdin.String(), `"content":"hello"`)

	// stdout mapped to session + assistant entry + completion.
	require.Len(t, evs, 3)
	require.NotNil(t, evs[0].Session)
	require.NotNil(t, evs[1].Entry)
	assert.Equal(t, "hi there", evs[1].Entry.Content)
	require.NotNil(t, evs[2].Complete)
	assert.Equal(t, 1000, evs[2].Complete.ContextWindow)
}

// TestChat_ContextCancel_Returns: cancelling ctx tears the transport down and
// returns even while the agent's stdout is still open (blocked read).
func TestChat_ContextCancel_Returns(t *testing.T) {
	pr, pw := io.Pipe() // stdout that never produces until closed
	var stdin bytes.Buffer

	b := &ClaudeCode{}
	b.openChatTransport = func(_ context.Context, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		return &chatTransport{
			stdin:  nopWriteCloser{&stdin},
			stdout: pr,
			close:  func() error { _ = pw.Close(); _ = pr.Close(); return nil }, // unblock the reader
		}, nil
	}

	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent)
	go func() { //nolint:revive // drain
		for range out {
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Chat(ctx, agent.ChatRequest{}, in, out) }()

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("Chat did not return after context cancel")
	}
}

// TestChat_TransportOpenError_Propagates: a spawn/open failure surfaces and out
// is still closed.
func TestChat_TransportOpenError_Propagates(t *testing.T) {
	b := &ClaudeCode{}
	b.openChatTransport = func(_ context.Context, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		return nil, io.ErrClosedPipe
	}
	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent)
	closed := make(chan struct{})
	go func() {
		for range out {
		}
		close(closed)
	}()
	err := b.Chat(context.Background(), agent.ChatRequest{}, in, out)
	require.Error(t, err)
	<-closed // out closed despite the open failure
}
