package browser

import (
	"net"
	"testing"
)

func TestReadSocksAddrDomain(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		// ATYP=0x03, length=7, "example", port=80.
		_, _ = server.Write([]byte{0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 0x00, 0x50})
	}()

	buf := make([]byte, 258)
	buf[3] = 0x03
	host, port, err := readSocksAddr(client, buf)
	if err != nil {
		t.Fatalf("readSocksAddr: %v", err)
	}
	if host != "example" || port != 80 {
		t.Fatalf("got %s:%d, want example:80", host, port)
	}
}

func TestReadSocksAddrIPv4(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		// ATYP=0x01, IPv4 1.2.3.4, port=443.
		_, _ = server.Write([]byte{1, 2, 3, 4, 0x01, 0xbb})
	}()

	buf := make([]byte, 258)
	buf[3] = 0x01
	host, port, err := readSocksAddr(client, buf)
	if err != nil {
		t.Fatalf("readSocksAddr: %v", err)
	}
	if host != "1.2.3.4" || port != 443 {
		t.Fatalf("got %s:%d, want 1.2.3.4:443", host, port)
	}
}
