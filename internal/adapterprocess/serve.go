package adapterprocess

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

// ServeOptions configures Serve. Schemas validates initialize and invoke
// requests against the protocol's RPC schemas.
type ServeOptions struct {
	Schemas *schemaregistry.Registry
}

// Serve runs one Go adapter implementation over jsonrpc-stdio-v1, reading
// requests from in and writing responses to out until in reaches EOF.
// Calls are handled one at a time. The local stream profile is never
// selected, so stream transfers are rejected.
func Serve(ctx context.Context, impl adapter.Implementation, in io.Reader, out io.Writer, options ServeOptions) error {
	if impl == nil || options.Schemas == nil {
		return errors.New("adapter implementation and schema registry are required")
	}
	server := &server{ctx: ctx, impl: impl, schemas: options.Schemas, out: out}
	frames := newFrameReader(in)
	for {
		frame, err := frames.next()
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case errors.Is(err, errBlankFrame), errors.Is(err, errOversizedFrame), errors.Is(err, errInvalidUTF8):
			if writeErr := server.fail(nil, codeParseError, err.Error(), nil); writeErr != nil {
				return writeErr
			}
			continue
		case err != nil:
			return err
		}
		if err := server.handle(frame); err != nil {
			return err
		}
	}
}

type server struct {
	ctx             context.Context
	impl            adapter.Implementation
	schemas         *schemaregistry.Registry
	out             io.Writer
	protocolVersion string
}

func (s *server) handle(frame []byte) error {
	message, id, code, err := parseEnvelope(frame)
	if err != nil {
		var rawID json.RawMessage
		if id != "" {
			rawID = message.ID
		}
		return s.fail(rawID, code, err.Error(), nil)
	}
	if message.Result != nil || message.Error != nil {
		return s.fail(message.ID, codeInvalidRequest, "responses are not accepted by the adapter", nil)
	}
	if message.Method != "adapter.initialize" && s.protocolVersion == "" {
		return s.fail(message.ID, codeInvalidRequest, "adapter.initialize must succeed first", nil)
	}
	switch message.Method {
	case "adapter.initialize":
		return s.initialize(message, frame)
	case "adapter.describe":
		return s.reply(message.ID, s.impl.Descriptor())
	case "adapter.health":
		return s.reply(message.ID, map[string]any{"status": "healthy", "observed_at": time.Now().UTC().Format(time.RFC3339Nano)})
	case "adapter.invoke":
		return s.invoke(message, id, frame)
	default:
		return s.fail(message.ID, codeMethodNotFound, "method is not supported by this adapter", nil)
	}
}

func (s *server) initialize(message envelope, frame []byte) error {
	if s.protocolVersion != "" {
		return s.fail(message.ID, codeInvalidRequest, "adapter is already initialized", nil)
	}
	if err := s.schemas.ValidateJSON("urn:gameserver-controller:"+s.schemas.ProtocolVersion()+":rpc:adapter-initialize-request", frame); err != nil {
		return s.fail(message.ID, codeInvalidParams, "initialize request does not match its schema", nil)
	}
	var params struct {
		ProtocolVersions []string `json:"protocol_versions"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return s.fail(message.ID, codeInvalidParams, "invalid initialize parameters", nil)
	}
	selected := ""
	for _, offered := range params.ProtocolVersions {
		for _, supported := range s.impl.Descriptor().ProtocolVersions {
			if offered == supported && selected == "" {
				selected = offered
			}
		}
	}
	if selected == "" {
		return s.fail(message.ID, codeServerError, "no mutually supported protocol version", protocolError("adapterprocess.unsupported_protocol_version", "compatibility_failure", "no mutually supported protocol version"))
	}
	session := make([]byte, 16)
	if _, err := rand.Read(session); err != nil {
		return s.fail(message.ID, codeInternalError, "could not create a session", nil)
	}
	s.protocolVersion = selected
	return s.reply(message.ID, map[string]any{"selected_protocol_version": selected, "session_id": "session-" + hex.EncodeToString(session)})
}

func (s *server) invoke(message envelope, id string, frame []byte) error {
	if err := s.schemas.ValidateJSON("urn:gameserver-controller:"+s.protocolVersion+":rpc:adapter-invoke-request", frame); err != nil {
		return s.fail(message.ID, codeInvalidParams, "invoke request does not match its schema", nil)
	}
	var request adapter.InvokeRequest
	if err := schemaregistry.DecodeStrictJSON(frame, &request); err != nil {
		return s.fail(message.ID, codeInvalidParams, "invalid invoke request", nil)
	}
	if request.Params.ProtocolVersion != s.protocolVersion {
		return s.fail(message.ID, codeInvalidParams, "invoke uses a protocol version other than the negotiated one", nil)
	}
	if len(request.Params.RequestContext.StreamTransfers) > 0 {
		return s.fail(message.ID, codeInvalidParams, "stream transfers need the local-unix-stream-v1 profile, which was not negotiated", nil)
	}
	callContext, cancel := context.WithDeadline(s.ctx, request.Params.RequestContext.Deadline)
	defer cancel()
	response, err := s.impl.Invoke(callContext, request)
	if err != nil {
		category := "internal"
		var invocation *adapter.InvocationError
		if errors.As(err, &invocation) && invocation.MayHaveSideEffects {
			category = "provider_failure"
		}
		// The message is deliberately generic; adapter errors may contain provider detail.
		return s.fail(message.ID, codeServerError, "capability invocation failed", protocolError("adapterprocess.invocation_failed", category, "capability invocation failed"))
	}
	if response.JSONRPC != "2.0" || response.ID != id {
		return s.fail(message.ID, codeInternalError, "adapter produced a response for a different request", nil)
	}
	return writeFrame(s.out, response)
}

func protocolError(code, category, message string) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{"code": code, "category": category, "message": message, "retryable": false})
	return encoded
}

func (s *server) reply(id json.RawMessage, result any) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return s.fail(id, codeInternalError, "could not encode result", nil)
	}
	return writeFrame(s.out, envelope{JSONRPC: "2.0", ID: id, Result: encoded})
}

func (s *server) fail(id json.RawMessage, code int, message string, data json.RawMessage) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return writeFrame(s.out, envelope{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message, Data: data}})
}
