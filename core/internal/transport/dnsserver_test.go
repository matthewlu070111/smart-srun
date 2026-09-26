package transport

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
)

// A DNS server small enough to be obviously correct, so the resolver can be
// tested against real queries on real sockets.
//
// It exists because the one property T13 is about -- that the TCP retry after a
// truncated UDP answer goes out by the same line -- cannot be observed without
// something that will actually truncate an answer. A stub that returns
// addresses would test nothing of the sort.
type testDNS struct {
	udp *net.UDPConn
	tcp net.Listener

	mu sync.Mutex
	// queries records the transport each question arrived on, in order, which
	// is how a test sees the fallback happen rather than inferring it.
	queries []string
	// truncateUDP makes the UDP answer set TC=1 and carry no records, which is
	// what a real server does when the answer will not fit.
	truncateUDP bool
	// answer is the address handed back for any A question.
	answer net.IP
	// silentUDP drops UDP queries entirely, for the timeout and rotation cases.
	silentUDP bool
}

func newTestDNS(t *testing.T) *testDNS {
	t.Helper()

	udpAddr, err := net.ResolveUDPAddr("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// The TCP side has to be on the same port as the UDP side, because a
	// resolver retries the same server address over TCP. The kernel picks the
	// UDP port, and the two port spaces are independent -- so the TCP bind can
	// collide with something else entirely, most often a socket an earlier test
	// in this process has not finished with. Asking for another UDP port is the
	// whole remedy; retrying makes `go test -count=2` work, which is how a flake
	// gets found in the first place.
	var udp *net.UDPConn
	var tcp net.Listener
	for attempt := range 20 {
		udp, err = net.ListenUDP("udp4", udpAddr)
		if err != nil {
			t.Fatalf("listen udp: %v", err)
		}
		tcp, err = net.Listen("tcp4", udp.LocalAddr().String())
		if err == nil {
			break
		}
		udp.Close()
		udp, tcp = nil, nil
		if attempt == 19 {
			t.Fatalf("no port was free on both udp and tcp: %v", err)
		}
	}

	server := &testDNS{udp: udp, tcp: tcp, answer: net.IPv4(203, 0, 113, 7)}
	go server.serveUDP()
	go server.serveTCP()
	t.Cleanup(func() { udp.Close(); tcp.Close() })
	return server
}

func (s *testDNS) port() int { return s.udp.LocalAddr().(*net.UDPAddr).Port }

func (s *testDNS) record(transport string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, transport)
}

func (s *testDNS) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

func (s *testDNS) configure(change func(*testDNS)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s)
}

func (s *testDNS) settings() (truncate, silent bool, answer net.IP) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.truncateUDP, s.silentUDP, s.answer
}

func (s *testDNS) serveUDP() {
	buffer := make([]byte, 1500)
	for {
		n, from, err := s.udp.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		s.record("udp")
		truncate, silent, answer := s.settings()
		if silent {
			continue
		}
		reply, ok := buildReply(buffer[:n], answer, truncate)
		if !ok {
			continue
		}
		s.udp.WriteToUDP(reply, from)
	}
}

func (s *testDNS) serveTCP() {
	for {
		conn, err := s.tcp.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			defer conn.Close()
			for {
				var length uint16
				if err := binary.Read(conn, binary.BigEndian, &length); err != nil {
					return
				}
				question := make([]byte, length)
				if _, err := io.ReadFull(conn, question); err != nil {
					return
				}
				s.record("tcp")
				_, _, answer := s.settings()
				// Never truncated over TCP: that is the whole point of the
				// retry.
				reply, ok := buildReply(question, answer, false)
				if !ok {
					return
				}
				if err := binary.Write(conn, binary.BigEndian,
					uint16(len(reply))); err != nil {
					return
				}
				if _, err := conn.Write(reply); err != nil {
					return
				}
			}
		}(conn)
	}
}

// buildReply turns a query into an answer, echoing the question section as a
// server must.
func buildReply(query []byte, answer net.IP, truncate bool) ([]byte, bool) {
	if len(query) < 12 {
		return nil, false
	}
	// Walk the question's labels to find where it ends.
	offset := 12
	for offset < len(query) {
		length := int(query[offset])
		if length == 0 {
			offset++
			break
		}
		if length&0xc0 != 0 {
			return nil, false
		}
		offset += length + 1
	}
	if offset+4 > len(query) {
		return nil, false
	}
	questionType := binary.BigEndian.Uint16(query[offset : offset+2])
	questionEnd := offset + 4

	reply := make([]byte, 0, questionEnd+16)
	reply = append(reply, query[:questionEnd]...)

	// QR=1 RD=1 RA=1, plus TC when the answer is being truncated.
	flags := uint16(0x8180)
	if truncate {
		flags |= 0x0200
	}
	binary.BigEndian.PutUint16(reply[2:4], flags)
	binary.BigEndian.PutUint16(reply[4:6], 1)

	// A truncated reply carries no records; that is what makes the client
	// retry over TCP. A question for anything but A gets an empty answer, so
	// the resolver does not treat an AAAA lookup as having succeeded.
	if truncate || questionType != 1 || answer.To4() == nil {
		binary.BigEndian.PutUint16(reply[6:8], 0)
		return reply, true
	}

	binary.BigEndian.PutUint16(reply[6:8], 1)
	record := make([]byte, 0, 16)
	record = append(record, 0xc0, 0x0c) // pointer back to the question's name
	record = append(record, 0x00, 0x01) // type A
	record = append(record, 0x00, 0x01) // class IN
	record = append(record, 0x00, 0x00, 0x00, 0x1e)
	record = append(record, 0x00, 0x04)
	record = append(record, answer.To4()...)
	return append(reply, record...), true
}
