package agent

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// The UI's port serves TLS, and answers plain HTTP with a redirect to HTTPS,
// so an old http:// link still works. As in LinuxIO: each connection's first
// byte is read in its own goroutine (0x16 opens a TLS handshake), so a client
// that sends nothing holds up no one.
const redirectTimeout = 5 * time.Second

type redirectListener struct {
	net.Listener
	cfg     *tls.Config
	conns   chan net.Conn
	done    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
	mu      sync.Mutex
	pending map[net.Conn]struct{}
	err     error
}

// NewRedirectListener serves TLS with cfg on inner and redirects plain HTTP.
func NewRedirectListener(inner net.Listener, cfg *tls.Config) net.Listener {
	l := &redirectListener{Listener: inner, cfg: cfg, conns: make(chan net.Conn), done: make(chan struct{}), pending: map[net.Conn]struct{}{}}
	l.wg.Go(l.acceptLoop)
	return l
}

func (l *redirectListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.err != nil {
			return nil, l.err
		}
		return nil, net.ErrClosed
	}
}

func (l *redirectListener) acceptLoop() {
	var wait time.Duration
	for {
		c, err := l.Listener.Accept()
		// A temporary error (too many open files) passes: wait and accept
		// again, as http.Server does, or the UI would be gone for good.
		if ne, ok := err.(interface{ Temporary() bool }); ok && ne.Temporary() && !errors.Is(err, net.ErrClosed) {
			wait = min(max(2*wait, 5*time.Millisecond), time.Second)
			select {
			case <-time.After(wait):
				continue
			case <-l.done:
				return
			}
		}
		wait = 0
		if err != nil {
			l.mu.Lock()
			l.err = err
			l.mu.Unlock()
			l.once.Do(func() { close(l.done) })
			return
		}
		l.mu.Lock()
		l.pending[c] = struct{}{}
		l.mu.Unlock()
		l.wg.Go(func() { l.classify(c) })
	}
}

func (l *redirectListener) classify(c net.Conn) {
	defer func() { l.mu.Lock(); delete(l.pending, c); l.mu.Unlock() }()
	br := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(redirectTimeout))
	first, err := br.Peek(1)
	if err != nil {
		_ = c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	pc := &peekedConn{Conn: c, r: br}
	if first[0] != 0x16 {
		redirect(pc)
		return
	}
	select {
	case l.conns <- tls.Server(pc, l.cfg):
	case <-l.done:
		_ = c.Close()
	}
}

// redirect answers one plain HTTP request with a 301 to the same host and path over HTTPS.
func redirect(c net.Conn) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(redirectTimeout))
	req, err := http.ReadRequest(bufio.NewReader(io.LimitReader(c, 8<<10)))
	if err != nil || req.Host == "" {
		return
	}
	target := "https://" + req.Host + req.RequestURI
	_, _ = fmt.Fprintf(c, "HTTP/1.1 301 Moved Permanently\r\nLocation: %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", target)
}

func (l *redirectListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { close(l.done) })
	l.mu.Lock()
	for c := range l.pending {
		_ = c.Close()
	}
	l.mu.Unlock()
	l.wg.Wait()
	return err
}

// peekedConn gives back the bytes the bufio.Reader already read.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(b []byte) (int, error) { return c.r.Read(b) }
