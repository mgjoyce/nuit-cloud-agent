package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetJSONUsesTheFallbackAddressLikeEveryOtherCall(t *testing.T) {
	allowLoopback(t)
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"a":1}`))
	}))
	defer srv.Close()
	c, _ := New("http://platform.invalid", "1", true)
	c.Credential = "nca_x"
	c.SetFallbacks([]string{strings.TrimPrefix(srv.URL, "http://")})
	var out struct{ A int }
	if err := c.GetJSON(context.Background(), "/x", 1024, &out); err != nil || out.A != 1 || auth != "Bearer nca_x" || !strings.HasPrefix(c.Route(), "by address ") {
		t.Fatalf("%v %+v %q %s", err, out, auth, c.Route())
	}
}

func TestGetJSONRefusesToFollowARedirectAndToReadMoreThanItWasToldTo(t *testing.T) {
	redirected := false
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			_, _ = w.Write([]byte(`{"pad":"` + strings.Repeat("x", 4096) + `"}`))
			return
		}
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "1", true)
	c.Credential = "nca_x"
	var out map[string]any
	var se *StatusError
	if err := c.GetJSON(context.Background(), "/r", 1024, &out); !errors.As(err, &se) || se.Status != 302 || redirected {
		t.Errorf("redirect: %v followed=%v", err, redirected)
	}
	if err := c.GetJSON(context.Background(), "/big", 1024, &out); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize: %v", err)
	}
}
