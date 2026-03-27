package zvt

import (
	"context"
	"log/slog"
	"net"
)

// Listener accepts TCP connections from ECR devices and hands them off to the Dispatcher.
type Listener struct {
	addr       string
	dispatcher *Dispatcher
}

// NewListener creates a Listener that will bind to addr and use dispatcher for command handling.
func NewListener(addr string, dispatcher *Dispatcher) *Listener {
	return &Listener{addr: addr, dispatcher: dispatcher}
}

// Listen binds the TCP listener and accepts connections until ctx is cancelled.
func (l *Listener) Listen(ctx context.Context) error {
	ln, err := net.Listen("tcp", l.addr)
	if err != nil {
		return err
	}
	slog.Info("zvt listener started", "addr", l.addr)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return err
		}
		slog.Info("ecr connected", "remote", conn.RemoteAddr().String())
		go NewSession(conn, l.dispatcher).Run(ctx)
	}
}
