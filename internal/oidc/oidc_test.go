package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProvider plays Authelia: discovery, JWKS, an authorization endpoint
// (the test reads the request from the redirect), a token endpoint that
// checks the client's secret and the PKCE verifier, and userinfo.
type fakeProvider struct {
	t      *testing.T
	srv    *httptest.Server
	rsa    *rsa.PrivateKey
	ec     *ecdsa.PrivateKey
	useEC  bool
	mu     sync.Mutex
	codes  map[string]authReq // code → the authorization request
	claims map[string]any     // extra ID token claims
	info   map[string]any     // userinfo claims
	tamper func(claims map[string]any)
	badSig bool
}

type authReq struct{ nonce, challenge string }

func newFake(t *testing.T) *fakeProvider {
	t.Helper()
	f := &fakeProvider{t: t, codes: map[string]authReq{}}
	f.rsa, _ = rsa.GenerateKey(rand.Reader, 2048)
	f.ec, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	mux := http.NewServeMux()
	// Like Cloudflare's bot protection in front of Authelia: Go's default
	// User-Agent gets 403.
	guard := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.UserAgent(), "Go-http-client") {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			h.ServeHTTP(w, r)
		})
	}
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "userinfo_endpoint": f.srv.URL + "/userinfo", "jwks_uri": f.srv.URL + "/jwks"})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		b := func(i *big.Int) string { return base64.RawURLEncoding.EncodeToString(i.Bytes()) }
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "RSA", "kid": "rsa1", "use": "sig", "alg": "RS256", "n": b(f.rsa.N), "e": b(big.NewInt(int64(f.rsa.E)))},
			{"kty": "EC", "kid": "ec1", "use": "sig", "crv": "P-256", "x": b(f.ec.X), "y": b(f.ec.Y)},
		}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok || id != "trapdoor" || secret != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
			return
		}
		_ = r.ParseForm()
		f.mu.Lock()
		req, ok := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != req.challenge {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "PKCE"})
			return
		}
		now := time.Now().Unix()
		c := map[string]any{"iss": f.srv.URL, "aud": []string{"trapdoor"}, "sub": "u-1", "exp": now + 300, "iat": now, "nonce": req.nonce, "preferred_username": "alex"}
		for k, v := range f.claims {
			c[k] = v
		}
		if f.tamper != nil {
			f.tamper(c)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": f.sign(c), "access_token": "at-1", "token_type": "Bearer"})
	})
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		info := map[string]any{"sub": "u-1"}
		for k, v := range f.info {
			info[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info)
	})
	f.srv = httptest.NewServer(guard(mux))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProvider) sign(c map[string]any) string {
	alg, kid := "RS256", "rsa1"
	if f.useEC {
		alg, kid = "ES256", "ec1"
	}
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	signed := enc(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"}) + "." + enc(c)
	sum := sha256.Sum256([]byte(signed))
	var sig []byte
	if f.useEC {
		r, s, _ := ecdsa.Sign(rand.Reader, f.ec, sum[:])
		sig = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	} else {
		sig, _ = rsa.SignPKCS1v15(rand.Reader, f.rsa, crypto.SHA256, sum[:])
	}
	if f.badSig {
		sig[0] ^= 0xff
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// login runs a sign-in like a browser: Start, the provider "approves",
// Finish with the returned code.
func (f *fakeProvider) login(p *Provider) (Identity, error) {
	state, to, err := p.Start(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	u, _ := url.Parse(to)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "trapdoor" || !strings.Contains(q.Get("scope"), "groups") ||
		q.Get("redirect_uri") != "https://vpn.example.com/api/v1/auth/oidc/callback" || q.Get("state") != state {
		f.t.Fatalf("authorization request: %s", to)
	}
	code := "code-" + state[:8]
	f.mu.Lock()
	f.codes[code] = authReq{nonce: q.Get("nonce"), challenge: q.Get("code_challenge")}
	f.mu.Unlock()
	return p.Finish(context.Background(), state, state, code)
}

func provider(t *testing.T, f *fakeProvider) *Provider {
	t.Helper()
	p, err := New(Config{Issuer: f.srv.URL + "/", ClientID: "trapdoor", ClientSecret: "s3cret",
		RedirectURL: "https://vpn.example.com/api/v1/auth/oidc/callback", Groups: []string{"admin", "vpn-admins"}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNew(t *testing.T) {
	ok := Config{Issuer: "https://auth.example.com", ClientID: "a", ClientSecret: "b", RedirectURL: "https://vpn.example.com/cb", Groups: []string{"admin"}}
	for name, c := range map[string]Config{
		"issuer":   {Issuer: "auth.example.com", ClientID: "a", ClientSecret: "b", RedirectURL: ok.RedirectURL, Groups: ok.Groups},
		"secret":   {Issuer: ok.Issuer, ClientID: "a", RedirectURL: ok.RedirectURL, Groups: ok.Groups},
		"redirect": {Issuer: ok.Issuer, ClientID: "a", ClientSecret: "b", Groups: ok.Groups},
		"groups":   {Issuer: ok.Issuer, ClientID: "a", ClientSecret: "b", RedirectURL: ok.RedirectURL},
		"method":   {Issuer: ok.Issuer, ClientID: "a", ClientSecret: "b", RedirectURL: ok.RedirectURL, Groups: ok.Groups, AuthMethod: "private_key_jwt"},
	} {
		if _, err := New(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	p, err := New(ok)
	if err != nil || strings.Join(p.cfg.Scopes, " ") != "openid profile email groups" {
		t.Errorf("defaults: %v %v", p, err)
	}
}

func TestSignIn(t *testing.T) {
	f := newFake(t)
	p := provider(t, f)

	// groups only in userinfo (Authelia's default claims policy)
	f.info = map[string]any{"groups": []string{"users", "admin"}, "email": "alex@example.com"}
	id, err := f.login(p)
	if err != nil || id.Username != "alex" || id.Email != "alex@example.com" || id.Subject != "u-1" {
		t.Fatalf("sign-in: %+v %v", id, err)
	}

	// groups in the ID token, signed with ES256
	f.useEC, f.info = true, nil
	f.claims = map[string]any{"groups": []string{"vpn-admins"}}
	if _, err := f.login(p); err != nil {
		t.Errorf("ES256: %v", err)
	}
	f.useEC = false

	// not in an allowed group
	f.claims = map[string]any{"groups": []string{"users"}}
	if id, err := f.login(p); !errors.Is(err, ErrNotAllowed) || id.Username != "alex" {
		t.Errorf("wrong group: %+v %v", id, err)
	}
	f.claims = map[string]any{"groups": []string{"admin"}}

	for name, tamper := range map[string]func(map[string]any){
		"expired":      func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"other issuer": func(c map[string]any) { c["iss"] = "https://evil.example.com" },
		"other client": func(c map[string]any) { c["aud"] = "someone-else" },
		"wrong nonce":  func(c map[string]any) { c["nonce"] = "replayed" },
		"future":       func(c map[string]any) { c["iat"] = time.Now().Add(time.Hour).Unix() },
		"no subject":   func(c map[string]any) { delete(c, "sub") },
	} {
		f.tamper = tamper
		if _, err := f.login(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	f.tamper = nil
	f.badSig = true
	if _, err := f.login(p); err == nil || !strings.Contains(err.Error(), "bad signature") {
		t.Errorf("bad signature: %v", err)
	}
	f.badSig = false

	// state: from another browser, unknown, or used twice
	state, _, _ := p.Start(context.Background())
	if _, err := p.Finish(context.Background(), "other", state, "x"); !errors.Is(err, ErrState) {
		t.Errorf("other browser: %v", err)
	}
	if _, err := p.Finish(context.Background(), state, state, "x"); !errors.Is(err, ErrState) {
		t.Errorf("used twice: %v", err) // the first try consumed it
	}

	// wrong client secret: the token endpoint refuses
	p2, _ := New(Config{Issuer: f.srv.URL, ClientID: "trapdoor", ClientSecret: "nope", RedirectURL: "https://vpn.example.com/api/v1/auth/oidc/callback", Groups: []string{"admin"}})
	if _, err := f.login(p2); err == nil || !strings.Contains(err.Error(), "invalid_client") {
		t.Errorf("wrong secret: %v", err)
	}
	// a proxy's refusal says who refused
	if e := refusedBy(&http.Response{Header: http.Header{"Server": {"cloudflare"}, "Cf-Mitigated": {"challenge"}}}, []byte("<html>\n Just a moment </html>")); !strings.Contains(e, "server=cloudflare") || !strings.Contains(e, "cf-mitigated=challenge") || !strings.Contains(e, `"<html> Just a moment </html>"`) {
		t.Errorf("refusedBy: %s", e)
	}
}

func TestDiscoveryIssuerMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": "https://other.example.com", "authorization_endpoint": "x", "token_endpoint": "y", "jwks_uri": "z"})
	}))
	defer srv.Close()
	p, _ := New(Config{Issuer: srv.URL, ClientID: "a", ClientSecret: "b", RedirectURL: "https://vpn.example.com/cb", Groups: []string{"admin"}})
	if _, _, err := p.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "calls itself") {
		t.Errorf("mismatch: %v", err)
	}
}

// A provider behind a certificate the system does not trust (e.g. reached
// through a proxy with a Cloudflare Origin certificate): RootCAs makes it work.
func TestRootCAs(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "authorization_endpoint": srv.URL + "/a", "token_endpoint": srv.URL + "/t", "jwks_uri": srv.URL + "/k"})
	}))
	defer srv.Close()
	cfg := Config{Issuer: srv.URL, ClientID: "a", ClientSecret: "b", RedirectURL: "https://vpn.example.com/cb", Groups: []string{"admin"}}
	p, _ := New(cfg)
	if _, _, err := p.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("untrusted certificate accepted: %v", err)
	}
	cfg.RootCAs = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, to, err := p.Start(context.Background()); err != nil || !strings.HasPrefix(to, srv.URL+"/a?") {
		t.Errorf("with the CA: %s %v", to, err)
	}
	cfg.RootCAs = []byte("not a certificate")
	if _, err := New(cfg); err == nil {
		t.Error("a CA file without a certificate accepted")
	}
}
