package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"time"
)

// StdioTransport spawns an upstream MCP server as a child process and proxies
// newline-delimited JSON-RPC messages bidirectionally over stdin/stdout.
type StdioTransport struct {
	Command        []string
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	ToolListFilter ToolListFilter
}

// NewStdioTransport returns a StdioTransport wired to os.Stdin/Stdout/Stderr.
func NewStdioTransport(command []string, filter ToolListFilter) *StdioTransport {
	return &StdioTransport{
		Command:        command,
		ToolListFilter: filter,
		Stdin:          os.Stdin,
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
	}
}

// Start spawns the upstream command and proxies JSON-RPC traffic until the client
// disconnects, the child exits, or the context is cancelled.
func (t *StdioTransport) Start(ctx context.Context, handler ToolCallHandler) error {
	if len(t.Command) == 0 {
		return fmt.Errorf("upstream command must not be empty")
	}

	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	cmd := exec.CommandContext(innerCtx, t.Command[0], t.Command[1:]...)
	setSysProcAttr(cmd)
	cmd.WaitDelay = 5 * time.Second
	cmd.Stderr = t.Stderr

	childIn, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating child stdin pipe: %w", err)
	}
	childOut, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating child stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting upstream process: %w", err)
	}

	slog.Debug("upstream process started", "pid", cmd.Process.Pid, "command", t.Command)

	// Shared writer for client stdout (both goroutines may write to it).
	sw := &syncWriter{w: t.Stdout}
	pending := newPendingCallbacks()
	filters := newPendingFilters()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		t.proxyClientToChild(innerCtx, t.Stdin, childIn, sw, handler, pending, filters)
	}()

	go func() {
		defer wg.Done()
		t.proxyChildToClient(childOut, sw, pending, filters)
	}()

	// Forward termination signals to the child process group.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, processSignals()...)
	go func() {
		select {
		case sig := <-sigCh:
			slog.Debug("forwarding signal to child", "signal", sig)
			killProcessGroup(cmd, sig)
			innerCancel()
		case <-innerCtx.Done():
		}
		signal.Stop(sigCh)
	}()

	waitErr := cmd.Wait()
	innerCancel()
	wg.Wait()

	if waitErr != nil {
		return fmt.Errorf("upstream process: %w", waitErr)
	}
	return nil
}

// proxyClientToChild reads lines from the client and forwards them to the child,
// intercepting tools/call messages via the handler.
func (t *StdioTransport) proxyClientToChild(ctx context.Context, client io.Reader, child io.WriteCloser, out *syncWriter, handler ToolCallHandler, pending *pendingCallbacks, filters *pendingFilters) {
	defer child.Close()

	scanner := bufio.NewScanner(client)
	scanner.Buffer(make([]byte, 0, 64*1024), maxScannerBuffer)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Bytes()

		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			// Not valid JSON; forward as-is.
			writeLineRaw(child, line)
			continue
		}

		if ir := interceptToolsCall(&msg, handler); ir != nil {
			if ir.denied {
				out.writeLine(ir.response)
				continue
			}
			if ir.onResponse != nil {
				pending.Add(msg.ID, ir.onResponse)
			}
		}

		registerToolListFilter(&msg, t.ToolListFilter, filters)

		writeLineRaw(child, line)
	}
}

// proxyChildToClient reads lines from the child and writes them to the client.
// For each line that looks like a JSON-RPC response (has ID, no method), it
// checks for a pending OnResponse callback and invokes it.
func (t *StdioTransport) proxyChildToClient(child io.Reader, out *syncWriter, pending *pendingCallbacks, filters *pendingFilters) {
	scanner := bufio.NewScanner(child)
	scanner.Buffer(make([]byte, 0, 64*1024), maxScannerBuffer)

	for scanner.Scan() {
		line := scanner.Bytes()

		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err == nil {
			if !msg.isRequest() && msg.ID != nil {
				if fn, ok := pending.Take(msg.ID); ok {
					fn(json.RawMessage(line))
				}
			}
		}

		line = applyFilter(line, filters)
		out.writeLine(line)
	}
}
