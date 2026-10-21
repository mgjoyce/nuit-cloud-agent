package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// allowLoopback lets a test use a server on 127.0.0.1 as a fallback (real fallbacks must be private addresses).
func allowLoopback(t *testing.T) {
	t.Helper()
	old := validFallback
	validFallback = func(s string) bool {
		host, _, err := net.SplitHostPort(s)
		return err == nil && (host == "127.0.0.1" || old(s))
	}
	t.Cleanup(func() { validFallback = old })
}

func platform(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		if status == 200 {
			_, _ = w.Write([]byte(`{"machineId":"m1","name":"box","pollIntervalSeconds":30,"tasks":[]}`))
		} else {
			_, _ = w.Write([]byte(`{"error":"no"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestWhenTheNameCannotBeReachedTheFallbackAddressIsUsedAndRemembered(t *testing.T) {
	allowLoopback(t)
	srv, hits := platform(t, 200)
	c, err := New("http://platform.invalid", "0.0.0", true) // a name that does not resolve
	if err != nil {
		t.Fatal(err)
	}
	if got := c.SetFallbacks([]string{strings.TrimPrefix(srv.URL, "http://")}); len(got) != 1 {
		t.Fatalf("fallback not kept: %v", got)
	}
	if _, err := c.CheckIn(context.Background()); err != nil {
		t.Fatalf("the fallback should have worked: %v", err)
	}
	if !strings.HasPrefix(c.Route(), "by address ") || hits.Load() != 1 {
		t.Errorf("route %q, hits %d", c.Route(), hits.Load())
	}
	// remembered: the next call goes to the address first, without trying the name again
	if _, err := c.CheckIn(context.Background()); err != nil || hits.Load() != 2 {
		t.Errorf("second call: %v hits=%d", err, hits.Load())
	}
}

func TestNoFallbackMeansTheNameFailsAsBefore(t *testing.T) {
	c, _ := New("http://platform.invalid", "0.0.0", true)
	if _, err := c.CheckIn(context.Background()); err == nil {
		t.Fatal("an unreachable platform with no fallback should be an error")
	}
	if c.Route() != "by name" {
		t.Errorf("route %q", c.Route())
	}
}

func TestAnAnsweringPlatformIsNeverRetriedElsewhereEvenWhenItRefuses(t *testing.T) {
	allowLoopback(t)
	refusing, refusals := platform(t, 401)
	other, otherHits := platform(t, 200)
	c, _ := New(refusing.URL, "0.0.0", true)
	c.SetFallbacks([]string{strings.TrimPrefix(other.URL, "http://")})
	_, err := c.CheckIn(context.Background())
	var se *StatusError
	if !errors.As(err, &se) || !se.Unauthorized() {
		t.Fatalf("want a 401, got %v", err)
	}
	if refusals.Load() != 1 || otherHits.Load() != 0 {
		t.Errorf("a refusal was taken for an outage: refusals=%d fallback hits=%d", refusals.Load(), otherHits.Load())
	}
}

func TestOnlyPrivateIPv4FallbacksAreKeptAtMostFour(t *testing.T) {
	c, _ := New("https://agent.example", "0.0.0", false)
	in := []string{"10.10.10.140:8443", "8.8.8.8:443", "agent.example:443", "10.10.10.141:0", "10.10.10.141:99999", "10.10.10.140:8443", "[fe80::1]:443", "192.168.1.5:443", "172.16.0.9:443", "10.0.0.1:443", "10.0.0.2:443", "nonsense"}
	got := c.SetFallbacks(in)
	want := []string{"10.10.10.140:8443", "192.168.1.5:443", "172.16.0.9:443", "10.0.0.1:443"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("kept %v, want %v", got, want)
	}
	if got := c.SetFallbacks(nil); len(got) != 0 {
		t.Errorf("an empty list should clear them: %v", got)
	}
}

func TestConnectingToAFixedAddressStillChecksTheCertificateAgainstThePlatformsName(t *testing.T) {
	allowLoopback(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	// the test server's certificate is for example.com and 127.0.0.1; ask for a name it is NOT for, through its address
	c, _ := New("https://not-the-platform.test", "0.0.0", false)
	c.HTTP.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool}
	c.SetFallbacks([]string{strings.TrimPrefix(srv.URL, "https://")})
	if _, err := c.CheckIn(context.Background()); err == nil {
		t.Fatal("an address that does not present a certificate for the platform's name was trusted")
	}
	// and for the name it IS for, it works
	ok, _ := New("https://example.com", "0.0.0", false)
	ok.HTTP.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool}
	ok.SetFallbacks([]string{strings.TrimPrefix(srv.URL, "https://")})
	if _, err := ok.CheckIn(context.Background()); err != nil {
		t.Fatalf("the right name through the address should work: %v", err)
	}
}
