package transport

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stallingListener accepts connections and never answers, as a peer whose TLS handshake
// hangs does. It counts the connections.
func stallingListener(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var conns []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			conns = append(conns, c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return ln.Addr().String(), &accepted
}

func noBackoff(int) time.Duration { return 0 }

func connectClient(scheme, host string, connect time.Duration) *Client {
	return New(scheme, host, WithHTTPClient(&http.Client{Transport: NewHTTPTransport(connect)}), WithRetry(3, noBackoff))
}

// A POST whose TLS handshake stalls past the connect timeout was never sent, so it is
// sent again: three connections, then a TimeoutError marked not sent.
func TestConnect_StalledHandshakeIsSentAgainWithinTheConnectTimeout(t *testing.T) {
	addr, accepted := stallingListener(t)
	c := connectClient("https", addr, 200*time.Millisecond)
	started := time.Now()
	_, err := c.Do(context.Background(), "POST", "/v1/sessions/main/desktop-token", Request{Token: "ct_x", Body: []byte(`{}`)})
	var te *TimeoutError
	if !errors.As(err, &te) || !NotSent(err) {
		t.Fatalf("err = %v (%T), want a TimeoutError marked not sent", err, err)
	}
	if got := accepted.Load(); got != 3 {
		t.Errorf("connections = %d, want 3", got)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("took %v, want about 3 × 200ms", elapsed)
	}
}

// A refused connection was never sent either.
func TestConnect_RefusedConnectionIsSentAgain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	var dials atomic.Int32
	tr := NewHTTPTransport(time.Second)
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		return dial(ctx, network, address)
	}
	c := New("http", addr, WithHTTPClient(&http.Client{Transport: tr}), WithRetry(3, noBackoff))
	_, err = c.Do(context.Background(), "POST", "/sessions/main/tabs", Request{Token: "ps_x", Body: []byte(`{}`)})
	var ce *ConnectionError
	if !errors.As(err, &ce) || !NotSent(err) {
		t.Fatalf("err = %v (%T), want a ConnectionError marked not sent", err, err)
	}
	if got := dials.Load(); got != 3 {
		t.Errorf("dials = %d, want 3", got)
	}
}

// A POST whose connection drops after it was sent is not sent again.
func TestConnect_FaultAfterSendingIsNotRetriedForAWrite(t *testing.T) {
	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		received.Add(1)
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(srv.Close)
	c := connectClient("http", strings.TrimPrefix(srv.URL, "http://"), time.Second)
	_, err := c.Do(context.Background(), "POST", "/sessions/main/tabs", Request{Token: "ps_x", Body: []byte(`{}`)})
	var ce *ConnectionError
	if !errors.As(err, &ce) || NotSent(err) {
		t.Fatalf("err = %v, want a ConnectionError that may have been sent", err)
	}
	if got := received.Load(); got != 1 {
		t.Errorf("server received %d requests, want 1", got)
	}
}

// A server hold extends the fallback deadline, so a read held past the request timeout
// alone still answers.
func TestDo_HoldExtendsTheFallbackDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	c := New("http", host, WithUnaryTimeout(200*time.Millisecond), WithRetry(1, nil))
	if _, err := c.Do(context.Background(), "GET", "/turn", Request{}); err == nil {
		t.Fatal("a read past the request timeout succeeded without a hold")
	}
	if _, err := c.Do(context.Background(), "GET", "/turn", Request{Hold: time.Second}); err != nil {
		t.Fatalf("held read: %v", err)
	}
}

// Successive requests reuse one connection.
func TestConnect_ReusesTheConnection(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	c := connectClient("http", strings.TrimPrefix(srv.URL, "http://"), time.Second)
	for range 5 {
		if _, err := c.Do(context.Background(), "GET", "/x", Request{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := conns.Load(); got != 1 {
		t.Errorf("connections = %d, want 1", got)
	}
}

func TestNewHTTPTransport_BoundsConnectAndHandshake(t *testing.T) {
	tr := NewHTTPTransport(7 * time.Second)
	if tr.TLSHandshakeTimeout != 7*time.Second || tr.DialContext == nil || tr.Proxy == nil {
		t.Errorf("transport = %+v", tr)
	}
}
