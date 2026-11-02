package gen

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveViteDevEnvHTTPS(t *testing.T) {
	port := freePort(t)
	otherPort := freePort(t)
	for _, tc := range []struct {
		name, raw, host, vitePort, want string
		wantWarning                     bool
	}{
		{name: "preserves scheme", raw: "https://mstudio:" + port, want: "https://mstudio:" + port},
		{name: "root path is an origin", raw: "https://mstudio:" + port + "/", want: "https://mstudio:" + port},
		{name: "host override preserves scheme", raw: "https://mstudio:" + port, host: "override", want: "https://override:" + port},
		{name: "port override preserves scheme", raw: "https://mstudio:" + otherPort, vitePort: port, want: "https://mstudio:" + port, wantWarning: true},
		{name: "IPv6 URL", raw: "https://[::1]:" + port, want: "https://[::1]:" + port},
		{name: "IPv6 host override", raw: "https://mstudio:" + port, host: "::1", want: "https://[::1]:" + port},
		{name: "IPv6 HTTP default", host: "::1", vitePort: port, want: "http://[::1]:" + port},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := []string{}
			if tc.raw != "" {
				env = append(env, "VITE_DEV_URL="+tc.raw)
			}
			if tc.vitePort != "" {
				env = append(env, "VITE_PORT="+tc.vitePort)
			}
			gotEnv, got, warning, err := resolveViteDevEnv(env, tc.host, "")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("origin = %q, want %q", got, tc.want)
			}
			if envValue(gotEnv, "VITE_DEV_URL", "") != tc.want {
				t.Errorf("child VITE_DEV_URL = %q, want %q", envValue(gotEnv, "VITE_DEV_URL", ""), tc.want)
			}
			if (warning != "") != tc.wantWarning {
				t.Errorf("warning = %q, want warning: %v", warning, tc.wantWarning)
			}
		})
	}

	t.Run("portless HTTPS reuses held port", func(t *testing.T) {
		_, got, _, err := resolveViteDevEnv([]string{"VITE_DEV_URL=https://mstudio"}, "", port)
		if err != nil || got != "https://mstudio:"+port {
			t.Fatalf("origin = %q, %v, want https://mstudio:%s", got, err, port)
		}
	})
}

func TestDevHTTPSManagement(t *testing.T) {
	cert, ca := devTestCertificate(t, true)
	trustDevTestCA(t, ca)
	const token = "https-session"
	posts := make(chan string, 8)
	var commandSent atomic.Bool
	srv := devTestTLSServer(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/healthz":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/__gsx/cmd":
			if r.Header.Get("x-gsx-token") != token {
				http.Error(w, "wrong token", http.StatusForbidden)
				return
			}
			w.Header().Set("x-gsx", token)
			if r.URL.Query().Get("wait") == "0" {
				w.WriteHeader(http.StatusNoContent)
			} else if !commandSent.Swap(true) {
				io.WriteString(w, `{"cmds":["rebuild"]}`)
			} else {
				<-r.Context().Done()
			}
		case r.Method == http.MethodPost && (r.URL.Path == "/__gsx/event" || r.URL.Path == "/__reload"):
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			posts <- r.URL.Path + ":" + string(body)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, origin, _, err := resolveViteDevEnv([]string{"VITE_DEV_URL=" + srv.URL}, "", port)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if !waitHealthy(ctx, origin+"/healthz", time.Second) {
		t.Fatal("trusted HTTPS front door failed its health probe")
	}
	if !verifyFrontDoor(ctx, origin, token) {
		t.Fatal("trusted HTTPS front door failed token verification")
	}

	p := newPoster(ctx)
	defer p.drain()
	p.postEvent(origin, []byte(`{"event":"generated"}`), nil)
	p.postReload(origin, nil)
	s := newStatusSender(ctx)
	defer s.drain()
	s.deposit(origin+"/__gsx/event", []byte(`{"event":"status"}`), nil)
	wantPosts := map[string]bool{
		`/__gsx/event:{"event":"generated"}`: true,
		`/__gsx/event:{"event":"status"}`:    true,
		"/__reload:":                         true,
	}
	for len(wantPosts) > 0 {
		select {
		case got := <-posts:
			if !wantPosts[got] {
				t.Fatalf("unexpected management POST %q", got)
			}
			delete(wantPosts, got)
		case <-time.After(3 * time.Second):
			t.Fatalf("HTTPS management POSTs not delivered: %v", wantPosts)
		}
	}

	out := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pollCommands(ctx, origin, token, func() bool { return true }, out, nil)
	}()
	defer func() { cancel(); <-done }()
	select {
	case got := <-out:
		if got != "rebuild" {
			t.Fatalf("HTTPS command = %q, want rebuild", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTPS command polling failed")
	}
}

func TestDevHTTPSRejectsInvalidCertificate(t *testing.T) {
	for _, tc := range []struct {
		name         string
		matchingHost bool
		trustCA      bool
	}{
		{name: "unknown authority", matchingHost: true},
		{name: "wrong hostname", trustCA: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert, ca := devTestCertificate(t, tc.matchingHost)
			if !tc.trustCA {
				ca = nil
			}
			trustDevTestCA(t, ca)
			var requests atomic.Int32
			srv := devTestTLSServer(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("x-gsx", "tok")
				w.WriteHeader(http.StatusNoContent)
			}))
			client := devHTTPClient(time.Second)
			resp, err := client.Get(srv.URL)
			if err == nil {
				resp.Body.Close()
				t.Fatal("unverified TLS certificate was accepted")
			}
			if tc.trustCA {
				if _, ok := errors.AsType[x509.HostnameError](err); !ok {
					t.Fatalf("wrong hostname error = %v, want x509.HostnameError", err)
				}
			} else {
				if _, ok := errors.AsType[x509.UnknownAuthorityError](err); !ok {
					t.Fatalf("untrusted CA error = %v, want x509.UnknownAuthorityError", err)
				}
			}
			if verifyFrontDoor(t.Context(), srv.URL, "tok") {
				t.Fatal("front door with invalid TLS certificate verified")
			}
			if waitHealthy(t.Context(), srv.URL+"/healthz", 100*time.Millisecond) {
				t.Fatal("health probe accepted invalid TLS certificate")
			}
			if requests.Load() != 0 {
				t.Fatal("management request reached a server with an invalid TLS certificate")
			}
		})
	}
}

// These tests remain serial because the production clients clone the process's
// default transport; only its trust roots change, preserving real TLS checks.
func trustDevTestCA(t *testing.T, ca *x509.Certificate) {
	t.Helper()
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	roots := x509.NewCertPool()
	if ca != nil {
		roots.AddCert(ca)
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: roots}
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = previous
		transport.CloseIdleConnections()
	})
}

func devTestTLSServer(t *testing.T, cert tls.Certificate, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func devTestCertificate(t *testing.T, matchingHost bool) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"wrong.example"},
	}
	if matchingHost {
		leaf.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: key}, ca
}

func TestResolveViteDevEnvRejectsInvalidOrigin(t *testing.T) {
	for _, raw := range []string{
		"ftp://mstudio:5173", "mstudio:5173", "//mstudio:5173", "https:mstudio",
		"https:///missing-host", "https://:5173", "https://user:pass@mstudio:5173",
		"https://mstudio:5173/app", "https://mstudio:5173/?x=1", "https://mstudio:5173/?",
		"https://mstudio:5173/#fragment", "https://mstudio:5173/#", "https://mstudio:",
		"https://mstudio:bad", "https://[::1", "https://mstudio:5173/%zz",
	} {
		t.Run(raw, func(t *testing.T) {
			_, _, _, err := resolveViteDevEnv([]string{"VITE_DEV_URL=" + raw}, "", "")
			if err == nil || !strings.Contains(err.Error(), "VITE_DEV_URL") {
				t.Fatalf("invalid origin %q: error = %v, want VITE_DEV_URL error", raw, err)
			}
		})
	}
}
