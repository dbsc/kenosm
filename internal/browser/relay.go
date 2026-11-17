package browser

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	"kenosm/internal/transport"
)

// socksRelay is a local SOCKS5 server that injects the upstream proxy's
// credentials. Headless Chromium connects to it via --proxy-server (which cannot
// supply SOCKS5 username/password itself); the relay dials the real SOCKS5 proxy
// with auth and then pipes bytes both ways.
type socksRelay struct {
	ln       net.Listener
	upstream proxy.Dialer
	wg       sync.WaitGroup
}

func startSocksRelay(up transport.ProxySpec) (*socksRelay, error) {
	var auth *proxy.Auth
	if up.Username != "" || up.Password != "" {
		auth = &proxy.Auth{User: up.Username, Password: up.Password}
	}
	dialer, err := proxy.SOCKS5("tcp", net.JoinHostPort(up.Host, strconv.Itoa(up.Port)), auth, &net.Dialer{Timeout: 20 * time.Second})
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &socksRelay{ln: ln, upstream: dialer}
	r.wg.Add(1)
	go r.serve()
	return r, nil
}

func (r *socksRelay) addr() string { return r.ln.Addr().String() }

func (r *socksRelay) close() {
	_ = r.ln.Close()
	r.wg.Wait()
}

func (r *socksRelay) serve() {
	defer r.wg.Done()
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.handle(c)
		}()
	}
}

func (r *socksRelay) handle(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 258)

	// Greeting: VER, NMETHODS, METHODS.
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return
	}
	if buf[0] != 0x05 {
		return
	}
	n := int(buf[1])
	if _, err := io.ReadFull(c, buf[:n]); err != nil {
		return
	}
	// Accept no-auth; Chromium is happy with it for a local proxy.
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	// Request: VER, CMD, RSV, ATYP, DST.ADDR, DST.PORT.
	if _, err := io.ReadFull(c, buf[:4]); err != nil {
		return
	}
	if buf[0] != 0x05 || buf[1] != 0x01 { // CONNECT only
		_, _ = c.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	host, port, err := readSocksAddr(c, buf)
	if err != nil {
		_, _ = c.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	up, err := r.upstream.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		_, _ = c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()

	// Success reply with a bound address of 0.0.0.0:0.
	if _, err := c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}

	errc := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); errc <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); errc <- struct{}{} }()
	<-errc
}

func readSocksAddr(c net.Conn, buf []byte) (host string, port int, err error) {
	switch buf[3] {
	case 0x01: // IPv4
		if _, err = io.ReadFull(c, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 0x03: // domain name
		if _, err = io.ReadFull(c, buf[:1]); err != nil {
			return
		}
		n := int(buf[0])
		if _, err = io.ReadFull(c, buf[:n]); err != nil {
			return
		}
		host = string(buf[:n])
	case 0x04: // IPv6
		if _, err = io.ReadFull(c, buf[:16]); err != nil {
			return
		}
		host = net.IP(buf[:16]).String()
	default:
		err = fmt.Errorf("unsupported address type %d", buf[3])
		return
	}
	if _, err = io.ReadFull(c, buf[:2]); err != nil {
		return
	}
	port = int(buf[0])<<8 | int(buf[1])
	return
}
