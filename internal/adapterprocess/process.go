package adapterprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

// ErrProcessFailed means the adapter process can no longer be trusted to keep
// requests and responses in step. In-flight mutations must be reconciled.
var ErrProcessFailed = errors.New("adapter process failed")

// Config describes how to start one adapter executable. The executable path
// must be absolute and is run directly, never through a shell. Env is the
// complete environment; nothing is inherited from the controller.
type Config struct {
	Path              string
	Args              []string
	Env               []string
	Dir               string
	ControllerID      string
	ControllerVersion string
	// StartTimeout bounds initialize and describe. StopTimeout bounds the
	// graceful exit after stdin closes. Both default to 10 seconds.
	StartTimeout time.Duration
	StopTimeout  time.Duration
}

// Process is a controller-side adapter.Implementation backed by a child
// process speaking jsonrpc-stdio-v1. Calls are serialized.
type Process struct {
	schemas    *schemaregistry.Registry
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	frames     chan frameResult
	exited     chan struct{}
	closed     chan struct{} // closed once the process is failed or closed; stops the reader
	closeOnce  sync.Once
	stderr     *boundedBuffer
	stop       time.Duration
	descriptor adapter.Descriptor

	mu     sync.Mutex
	nextID uint64
	failed error
}

type frameResult struct {
	frame []byte
	err   error
}

// Start launches the adapter, negotiates the protocol version, and reads and
// checks its descriptor. The caller should then pass the Process to
// adapter.Registry.Register, which validates the descriptor against the schema.
func Start(ctx context.Context, schemas *schemaregistry.Registry, config Config) (*Process, error) {
	if schemas == nil || !filepath.IsAbs(config.Path) || config.ControllerID == "" || config.ControllerVersion == "" {
		return nil, errors.New("schema registry, absolute adapter path, and controller identity are required")
	}
	startTimeout, stopTimeout := orDefault(config.StartTimeout), orDefault(config.StopTimeout)
	cmd := exec.Command(config.Path, config.Args...)
	cmd.Env = append([]string{}, config.Env...) // non-nil: never inherit the controller environment
	cmd.Dir = config.Dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// An owned pipe, unlike StdoutPipe, is not closed by Wait, so frames
	// written just before the child exits can still be read.
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = childStdout
	stderr := &boundedBuffer{limit: 8 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = childStdout.Close()
		return nil, fmt.Errorf("could not start adapter process: %w", err)
	}
	_ = childStdout.Close()
	p := &Process{schemas: schemas, cmd: cmd, stdin: stdin, frames: make(chan frameResult), exited: make(chan struct{}),
		closed: make(chan struct{}), stderr: stderr, stop: stopTimeout}
	go p.readFrames(stdout)
	go func() { _ = cmd.Wait(); close(p.exited) }()

	startContext, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	if err := p.handshake(startContext, config); err != nil {
		p.kill()
		return nil, err
	}
	return p, nil
}

func orDefault(value time.Duration) time.Duration {
	if value <= 0 {
		return 10 * time.Second
	}
	return value
}

func (p *Process) handshake(ctx context.Context, config Config) error {
	version := p.schemas.ProtocolVersion()
	frame, err := p.call(ctx, "adapter.initialize", map[string]any{
		"protocol_versions": []string{version},
		"controller":        map[string]string{"implementation_id": config.ControllerID, "implementation_version": config.ControllerVersion},
	})
	if err != nil {
		return fmt.Errorf("adapter initialize failed: %w", err)
	}
	if err := p.schemas.ValidateJSON("urn:gameserver-controller:"+version+":rpc:adapter-initialize-result", frame); err != nil {
		return fmt.Errorf("adapter initialize result is invalid: %w", err)
	}
	var initialized struct {
		Result struct {
			SelectedProtocolVersion string `json:"selected_protocol_version"`
			SelectedStreamTransport string `json:"selected_stream_transport"`
		} `json:"result"`
	}
	if err := json.Unmarshal(frame, &initialized); err != nil || initialized.Result.SelectedProtocolVersion != version {
		return errors.New("adapter selected a protocol version the controller did not offer")
	}
	if initialized.Result.SelectedStreamTransport != "" {
		return errors.New("adapter selected a stream transport the controller did not offer")
	}

	frame, err = p.call(ctx, "adapter.describe", map[string]any{})
	if err != nil {
		return fmt.Errorf("adapter describe failed: %w", err)
	}
	if err := p.schemas.ValidateJSON("urn:gameserver-controller:"+version+":rpc:adapter-describe-result", frame); err != nil {
		return fmt.Errorf("adapter descriptor is invalid: %w", err)
	}
	var described struct {
		JSONRPC string             `json:"jsonrpc"`
		ID      string             `json:"id"`
		Result  adapter.Descriptor `json:"result"`
	}
	if err := schemaregistry.DecodeStrictJSON(frame, &described); err != nil {
		return fmt.Errorf("adapter descriptor is invalid: %w", err)
	}
	for _, capability := range described.Result.Capabilities {
		if capability.Name == "storage.object.put" || capability.Name == "storage.object.get" {
			return fmt.Errorf("adapter advertises %s, which needs the local-unix-stream-v1 profile that this transport does not negotiate", capability.Name)
		}
	}
	p.descriptor = described.Result
	return nil
}

// Descriptor returns the descriptor read during Start.
func (p *Process) Descriptor() adapter.Descriptor { return p.descriptor }

// Invoke sends one adapter.invoke request and waits for its response. Any
// transport failure marks the process failed; the registry then reports the
// call as possibly having side effects when the capability is mutating.
func (p *Process) Invoke(ctx context.Context, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	if len(request.Params.RequestContext.StreamTransfers) > 0 {
		return adapter.InvokeResponse{}, errors.New("stream transfers are not supported by this process transport")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	frame, err := p.exchange(ctx, request.ID, request)
	if err != nil {
		return adapter.InvokeResponse{}, err
	}
	var response adapter.InvokeResponse
	if err := schemaregistry.DecodeStrictJSON(frame, &response); err != nil {
		p.markFailed(errors.New("adapter sent a malformed invoke response"))
		return adapter.InvokeResponse{}, p.failed
	}
	return response, nil
}

// Health asks the adapter for its health report.
func (p *Process) Health(ctx context.Context) (string, error) {
	frame, err := p.call(ctx, "adapter.health", map[string]any{})
	if err != nil {
		return "", err
	}
	if err := p.schemas.ValidateJSON("urn:gameserver-controller:"+p.schemas.ProtocolVersion()+":rpc:adapter-health-result", frame); err != nil {
		return "", errors.New("adapter health result is invalid")
	}
	var health struct {
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	_ = json.Unmarshal(frame, &health)
	return health.Result.Status, nil
}

// call sends a controller-generated request and returns the full response frame.
func (p *Process) call(ctx context.Context, method string, params any) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	id := "gsc-" + strconv.FormatUint(p.nextID, 10)
	return p.exchange(ctx, id, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

// exchange writes one request and reads its response. The caller holds mu.
func (p *Process) exchange(ctx context.Context, id string, request any) ([]byte, error) {
	if p.failed != nil {
		return nil, p.failed
	}
	written := make(chan error, 1)
	go func() { written <- writeFrame(p.stdin, request) }()
	select {
	case err := <-written:
		if err != nil {
			p.markFailed(errors.New("could not write to the adapter process"))
			return nil, p.failed
		}
	case <-ctx.Done():
		p.markFailed(errors.New("deadline reached while sending to the adapter process"))
		return nil, p.failed
	}
	var result frameResult
	select {
	case result = <-p.frames:
	case <-ctx.Done():
		// A late response could otherwise be matched to the next request.
		p.markFailed(errors.New("deadline reached before the adapter responded"))
		return nil, p.failed
	}
	if result.err != nil {
		p.markFailed(fmt.Errorf("adapter output stopped: %w", result.err))
		return nil, p.failed
	}
	message, responseID, _, err := parseEnvelope(result.frame)
	if err != nil || responseID != id || message.Method != "" || (message.Result == nil) == (message.Error == nil) {
		p.markFailed(errors.New("adapter wrote an unexpected or malformed frame to stdout"))
		return nil, p.failed
	}
	if message.Error != nil {
		return nil, message.Error
	}
	return result.frame, nil
}

func (p *Process) readFrames(stdout io.ReadCloser) {
	defer stdout.Close()
	frames := newFrameReader(stdout)
	for {
		frame, err := frames.next()
		select {
		case p.frames <- frameResult{frame: frame, err: err}:
		case <-p.closed:
			return
		}
		if err != nil {
			return
		}
	}
}

// markFailed records the first failure and stops the process. The caller holds mu.
func (p *Process) markFailed(cause error) {
	if p.failed == nil {
		p.failed = fmt.Errorf("%w: %v", ErrProcessFailed, cause)
		p.kill()
	}
}

func (p *Process) kill() {
	p.closeOnce.Do(func() { close(p.closed) })
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// Close asks the adapter to exit by closing stdin, then kills it after the
// stop timeout. Killing the process does not prove provider work stopped.
func (p *Process) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed == nil {
		p.failed = fmt.Errorf("%w: closed", ErrProcessFailed)
	}
	_ = p.stdin.Close()
	select {
	case <-p.exited:
		p.closeOnce.Do(func() { close(p.closed) })
		return nil
	case <-time.After(p.stop):
		p.kill()
		<-p.exited
		return errors.New("adapter process did not exit after stdin closed; it was killed")
	}
}

// Diagnostics returns the last few kilobytes the adapter wrote to stderr.
// Adapters must redact stderr, but callers should still treat it as untrusted.
func (p *Process) Diagnostics() string { return p.stderr.String() }

type boundedBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, data...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(data), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
