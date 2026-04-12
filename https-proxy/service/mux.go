package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
)

// peekConn wraps a connection so TLS reads from a bufio.Reader after Peek.
type peekConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *peekConn) Read(b []byte) (int, error) { return p.r.Read(b) }

// tlsPeekListener accepts TCP connections: TLS ClientHello (0x16) → TLS server conn;
// otherwise plain HTTP → 308 redirect to https://same host/path (fixes "HTTP request to HTTPS server").
type tlsPeekListener struct {
	inner  net.Listener
	tlsCfg *tls.Config
}

func (l *tlsPeekListener) Accept() (net.Conn, error) {
	for {
		c, err := l.inner.Accept()
		if err != nil {
			return nil, err
		}
		br := bufio.NewReader(c)
		head, err := br.Peek(1)
		if err != nil {
			_ = c.Close()
			continue
		}
		// TLS record type handshake = 0x16
		if head[0] == 0x16 {
			return tls.Server(&peekConn{Conn: c, r: br}, l.tlsCfg), nil
		}
		go serveHTTPToHTTPSRedirect(c, br)
	}
}

func (l *tlsPeekListener) Close() error { return l.inner.Close() }

func (l *tlsPeekListener) Addr() net.Addr { return l.inner.Addr() }

func listenHTTPSOrRedirect(addr string, tlsCfg *tls.Config) (net.Listener, error) {
	plain, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &tlsPeekListener{inner: plain, tlsCfg: tlsCfg}, nil
}

func serveHTTPToHTTPSRedirect(c net.Conn, br *bufio.Reader) {
	defer c.Close()
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Body != nil {
		_ = req.Body.Close()
	}

	host := req.Host
	if host == "" {
		if ta, ok := c.LocalAddr().(*net.TCPAddr); ok {
			host = fmt.Sprintf("127.0.0.1:%d", ta.Port)
		} else {
			host = "127.0.0.1"
		}
	}

	path := req.URL.RequestURI()
	if path == "" {
		path = "/"
	}
	loc := "https://" + host + path

	_, _ = fmt.Fprintf(c, "HTTP/1.1 308 Permanent Redirect\r\n"+
		"Location: %s\r\n"+
		"Connection: close\r\n"+
		"Content-Length: 0\r\n"+
		"\r\n", loc)
}
