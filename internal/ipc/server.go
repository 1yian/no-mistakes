package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"
)

type HandlerFunc func(ctx context.Context, params json.RawMessage) (interface{}, error)

type StreamFunc func(send func(interface{}) error) error

type StreamHandlerFunc func(ctx context.Context, params json.RawMessage) (StreamFunc, error)

type Server struct {
	mu             sync.RWMutex
	handlers       map[string]HandlerFunc
	streamHandlers map[string]StreamHandlerFunc
	listener       net.Listener
	wg             sync.WaitGroup
	done           chan struct{}
	closeOnce      sync.Once
}

func NewServer() *Server {
	return &Server{
		handlers:       make(map[string]HandlerFunc),
		streamHandlers: make(map[string]StreamHandlerFunc),
		done:           make(chan struct{}),
	}
}

func (s *Server) Handle(method string, fn HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = fn
}

func (s *Server) HandleStream(method string, fn StreamHandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamHandlers[method] = fn
}

func (s *Server) Listen(socketPath string) error {
	ln, err := listen(socketPath)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.listener != nil {
		s.mu.Unlock()
		_ = ln.Close()
		return errors.New("IPC server already listening")
	}
	s.listener = ln
	s.mu.Unlock()

	go func() {
		<-s.done
		_ = ln.Close()
	}()
	return nil
}

func (s *Server) Serve(socketPath string) error {
	if err := s.Listen(socketPath); err != nil {
		return err
	}
	return s.ServeReady()
}

func (s *Server) ServeReady() error {
	s.mu.RLock()
	ln := s.listener
	s.mu.RUnlock()
	if ln == nil {
		return errors.New("IPC server is not listening")
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				s.wg.Wait()
				return nil
			default:
				if errors.Is(err, net.ErrClosed) {
					s.Close()
					s.wg.Wait()
					return nil
				}
				slog.Error("accept connection", "error", err)
				continue
			}
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn)
		}()
	}
}

func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
	})
}

func (s *Server) CloseListener() {
	s.mu.RLock()
	ln := s.listener
	s.mu.RUnlock()
	if ln != nil {
		ln.Close()
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, MaxFrameBytes), MaxFrameBytes)
	encoder := json.NewEncoder(conn)

	// Bind OS-authenticated peer identity to every request on this connection.
	// Handlers use it for process-ancestry authorization; it is never accepted
	// from request parameters or environment variables.
	ctx := withPeerPID(context.Background(), authenticatedPeerPID(conn))
	// Create a context that cancels when the server shuts down.
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	defer cancel()

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			slog.Warn("ipc request failed", "method", "<parse>", "error", "invalid json")
			resp := NewErrorResponse(0, ErrParseError, "invalid json")
			encoder.Encode(resp)
			continue
		}

		// Check for stream handler first.
		s.mu.RLock()
		streamHandler, isStream := s.streamHandlers[req.Method]
		s.mu.RUnlock()

		if isStream {
			streamCtx, streamCancel := requestContext(ctx, req)
			stream, err := streamHandler(streamCtx, req.Params)
			if err != nil {
				slog.Warn("ipc stream request failed", "method", req.Method, "error", err)
				_ = encoder.Encode(NewErrorResponse(req.ID, ErrInternal, err.Error()))
				streamCancel()
				return
			}
			defer streamCancel()
			slog.Info("ipc stream request", "method", req.Method)
			// Acknowledge only after stream preparation has registered its event
			// source, so the client can safely reconcile as soon as this arrives.
			resp, _ := NewResponse(req.ID, map[string]bool{"ok": true})
			if err := encoder.Encode(resp); err != nil {
				slog.Error("write stream response", "error", err)
				return
			}
			peerClosed := make(chan struct{})
			go func() {
				defer close(peerClosed)
				for scanner.Scan() {
				}
				cancel()
			}()
			send := func(event interface{}) error {
				return encoder.Encode(event)
			}
			streamErr := stream(send)
			_ = conn.Close()
			<-peerClosed
			if streamErr != nil {
				slog.Warn("ipc stream request failed", "method", req.Method, "error", streamErr)
			}
			return // connection done after streaming
		}

		resp := s.dispatch(ctx, req)
		if err := encoder.Encode(resp); err != nil {
			slog.Error("write response", "error", err)
			return
		}
	}
}

type requestDeadlineContextKey struct{}

func RequestDeadline(ctx context.Context) (time.Time, bool) {
	deadline, ok := ctx.Value(requestDeadlineContextKey{}).(time.Time)
	return deadline, ok
}

func requestContext(parent context.Context, req Request) (context.Context, context.CancelFunc) {
	if req.DeadlineUnixNano == 0 {
		return parent, func() {}
	}
	return context.WithValue(parent, requestDeadlineContextKey{}, time.Unix(0, req.DeadlineUnixNano)), func() {}
}

func (s *Server) dispatch(ctx context.Context, req Request) *Response {
	s.mu.RLock()
	handler, ok := s.handlers[req.Method]
	s.mu.RUnlock()

	if !ok {
		err := "method not found: " + req.Method
		slog.Warn("ipc request failed", "method", req.Method, "error", err)
		return NewErrorResponse(req.ID, ErrMethodNotFound, err)
	}

	requestCtx, cancel := requestContext(ctx, req)
	defer cancel()
	result, err := handler(requestCtx, req.Params)
	if err != nil {
		slog.Warn("ipc request failed", "method", req.Method, "error", err)
		return NewErrorResponse(req.ID, ErrInternal, err.Error())
	}

	resp, err := NewResponse(req.ID, result)
	if err != nil {
		slog.Warn("ipc request failed", "method", req.Method, "error", err)
		return NewErrorResponse(req.ID, ErrInternal, "failed to marshal result")
	}
	if readOnlyMethod(req.Method) {
		slog.Debug("ipc request", "method", req.Method)
	} else {
		slog.Info("ipc request", "method", req.Method)
	}
	return resp
}

func readOnlyMethod(method string) bool {
	switch method {
	case MethodHealth, MethodProbeOmitIntent, MethodProbeRunPRText, MethodGetRun, MethodGetStepDiff, MethodGetRuns, MethodGetRunsForHead, MethodGetActiveRun, MethodGateContext, MethodAdmitPush:
		return true
	default:
		return false
	}
}
