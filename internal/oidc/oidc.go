// Package oidc signs admins in with an OpenID Connect provider such as
// Authelia: the authorization code flow with PKCE (S256), state and nonce,
// the ID token's signature checked against the provider's keys (RS256/384/
// 512, ES256/384/512), and the userinfo endpoint read for the claims that
// providers leave out of the ID token (Authelia does for groups).
//
// stdlib only: discovery, JWKS and JWT are small enough to do here.
package oidc

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	_ "crypto/sha512" // SHA-384 / SHA-512 for RS384, ES512, …
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config is what the web part is started with (TD_OIDC_*).
type Config struct {
	Issuer       string   // e.g. https://auth.example.com
	ClientID     string   // as registered at the provider
	ClientSecret string   // its secret (the provider keeps a hash)
	RedirectURL  string   // <TD_PUBLIC_URL>/api/v1/auth/oidc/callback
	Scopes       []string // default: openid profile email groups
	Groups       []string // a member of any of these may sign in
	GroupsClaim  string   // default "groups"
	// AuthMethod at the token endpoint: client_secret_basic (default) or
	// client_secret_post.
	AuthMethod string
	HTTP       *http.Client // nil: a client with a 10 s timeout
	// UserAgent of the requests to the provider (default "trapdoor"): Go's
	// own is blocked by some proxies in front of providers (Cloudflare's
	// bot protection answers the token request with 403).
	UserAgent string
	Now       func() time.Time // nil: time.Now
}

// Identity is who signed in.
type Identity struct {
	Subject  string
	Username string // preferred_username (or the subject)
	Email    string
	Groups   []string
}

// Errors the web part tells apart.
var (
	ErrNotAllowed = errors.New("not in an allowed group")
	ErrState      = errors.New("the sign-in expired or came from another browser, try again")
)

// Provider is one configured provider. Its metadata and keys are fetched
// when first needed (the provider may start after trapdoor) and the keys
// again when a token names an unknown one (key rotation).
type Provider struct {
	cfg Config

	mu   sync.Mutex
	meta *metadata
	keys map[string]crypto.PublicKey // by kid ("" when the provider sets none)
	// pending sign-ins by state: their verifier and nonce, 10 minutes.
	pending map[string]pending
}

type pending struct {
	verifier, nonce string
	until           time.Time
}

type metadata struct {
	Issuer   string `json:"issuer"`
	Auth     string `json:"authorization_endpoint"`
	Token    string `json:"token_endpoint"`
	UserInfo string `json:"userinfo_endpoint"`
	JWKS     string `json:"jwks_uri"`
}

// New checks the configuration; nothing is fetched yet.
func New(cfg Config) (*Provider, error) {
	cfg.Issuer = strings.TrimRight(cfg.Issuer, "/")
	if u, err := url.Parse(cfg.Issuer); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("issuer %q is not an http(s) address", cfg.Issuer)
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("client id and secret are needed")
	}
	if u, err := url.Parse(cfg.RedirectURL); err != nil || u.Host == "" {
		return nil, fmt.Errorf("redirect URL %q is not an address (set TD_PUBLIC_URL)", cfg.RedirectURL)
	}
	if len(cfg.Groups) == 0 {
		return nil, errors.New("at least one allowed group is needed")
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "profile", "email", "groups"}
	}
	if !slices.Contains(cfg.Scopes, "openid") {
		cfg.Scopes = append([]string{"openid"}, cfg.Scopes...)
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	switch cfg.AuthMethod {
	case "":
		cfg.AuthMethod = "client_secret_basic"
	case "client_secret_basic", "client_secret_post":
	default:
		return nil, fmt.Errorf("token endpoint auth method %q: client_secret_basic or client_secret_post", cfg.AuthMethod)
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "trapdoor"
	}
	return &Provider{cfg: cfg, pending: map[string]pending{}}, nil
}

// Start begins a sign-in: the state to keep in the browser's cookie and
// the provider's address to send the browser to.
func (p *Provider) Start(ctx context.Context) (state, to string, err error) {
	meta, err := p.metadata(ctx)
	if err != nil {
		return "", "", err
	}
	state, nonce, verifier := random(), random(), random()
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {p.cfg.ClientID},
		"redirect_uri":          {p.cfg.RedirectURL},
		"scope":                 {strings.Join(p.cfg.Scopes, " ")},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	now := p.cfg.Now()
	p.mu.Lock()
	for k, v := range p.pending {
		if now.After(v.until) {
			delete(p.pending, k)
		}
	}
	if len(p.pending) >= 1000 { // a flood of unfinished sign-ins: start over
		p.pending = map[string]pending{}
	}
	p.pending[state] = pending{verifier: verifier, nonce: nonce, until: now.Add(10 * time.Minute)}
	p.mu.Unlock()
	sep := "?"
	if strings.Contains(meta.Auth, "?") {
		sep = "&"
	}
	return state, meta.Auth + sep + q.Encode(), nil
}

// Finish completes a sign-in with the code from the callback. cookieState
// is the state kept in the browser; it must match the callback's.
func (p *Provider) Finish(ctx context.Context, cookieState, state, code string) (Identity, error) {
	// A state is used once, whatever comes of it.
	p.mu.Lock()
	pend, ok := p.pending[state]
	delete(p.pending, state)
	p.mu.Unlock()
	if !ok || state == "" || state != cookieState || p.cfg.Now().After(pend.until) {
		return Identity{}, ErrState
	}
	meta, err := p.metadata(ctx)
	if err != nil {
		return Identity{}, err
	}
	tok, err := p.exchange(ctx, meta, code, pend.verifier)
	if err != nil {
		return Identity{}, err
	}
	claims, err := p.verify(ctx, tok.IDToken, pend.nonce)
	if err != nil {
		return Identity{}, fmt.Errorf("ID token: %w", err)
	}
	if meta.UserInfo != "" && tok.AccessToken != "" {
		info, err := p.userinfo(ctx, meta.UserInfo, tok.AccessToken)
		if err != nil {
			return Identity{}, fmt.Errorf("userinfo: %w", err)
		}
		if sub, _ := info["sub"].(string); sub != claims["sub"] {
			return Identity{}, errors.New("userinfo is about someone else")
		}
		for k, v := range info { // the ID token's own claims win
			if _, ok := claims[k]; !ok {
				claims[k] = v
			}
		}
	}
	id := identity(claims, p.cfg.GroupsClaim)
	if !slices.ContainsFunc(id.Groups, func(g string) bool { return slices.Contains(p.cfg.Groups, g) }) {
		return id, ErrNotAllowed
	}
	return id, nil
}

func identity(c map[string]any, groupsClaim string) Identity {
	str := func(k string) string { s, _ := c[k].(string); return s }
	id := Identity{Subject: str("sub"), Username: str("preferred_username"), Email: str("email")}
	if id.Username == "" {
		id.Username = id.Subject
	}
	switch g := c[groupsClaim].(type) {
	case []any:
		for _, v := range g {
			if s, ok := v.(string); ok {
				id.Groups = append(id.Groups, s)
			}
		}
	case string: // some providers send one group as a string
		id.Groups = []string{g}
	}
	return id
}

func (p *Provider) metadata(ctx context.Context) (*metadata, error) {
	p.mu.Lock()
	m := p.meta
	p.mu.Unlock()
	if m != nil {
		return m, nil
	}
	var meta metadata
	if err := p.getJSON(ctx, p.cfg.Issuer+"/.well-known/openid-configuration", "", &meta); err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	if strings.TrimRight(meta.Issuer, "/") != p.cfg.Issuer {
		return nil, fmt.Errorf("discovery: the provider calls itself %q, not %q", meta.Issuer, p.cfg.Issuer)
	}
	if meta.Auth == "" || meta.Token == "" || meta.JWKS == "" {
		return nil, errors.New("discovery: endpoints missing")
	}
	meta.Issuer = p.cfg.Issuer
	p.mu.Lock()
	p.meta = &meta
	p.mu.Unlock()
	return &meta, nil
}

type tokens struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

func (p *Provider) exchange(ctx context.Context, meta *metadata, code, verifier string) (tokens, error) {
	var out tokens
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {p.cfg.RedirectURL}, "code_verifier": {verifier}}
	if p.cfg.AuthMethod == "client_secret_post" {
		form.Set("client_id", p.cfg.ClientID)
		form.Set("client_secret", p.cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.cfg.UserAgent)
	if p.cfg.AuthMethod == "client_secret_basic" {
		req.SetBasicAuth(url.QueryEscape(p.cfg.ClientID), url.QueryEscape(p.cfg.ClientSecret))
	}
	resp, err := p.cfg.HTTP.Do(req)
	if err != nil {
		return out, fmt.Errorf("token endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode != http.StatusOK || out.IDToken == "" {
		msg := out.Error
		if out.Description != "" {
			msg += ": " + out.Description
		}
		if msg == "" {
			msg = resp.Status + refusedBy(resp, body)
		}
		return out, fmt.Errorf("token endpoint: %s", msg)
	}
	return out, nil
}

// refusedBy says who answered a request with something other than the
// provider's JSON: a proxy in front of it (e.g. Cloudflare's challenge).
func refusedBy(resp *http.Response, body []byte) string {
	var parts []string
	for _, h := range []string{"Server", "Cf-Mitigated", "Cf-Ray", "Content-Type"} {
		if v := resp.Header.Get(h); v != "" {
			parts = append(parts, strings.ToLower(h)+"="+v)
		}
	}
	text := strings.Join(strings.Fields(string(body)), " ")
	if len(text) > 160 {
		text = text[:160]
	}
	if text != "" {
		parts = append(parts, "body="+strconv.Quote(text))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func (p *Provider) userinfo(ctx context.Context, endpoint, access string) (map[string]any, error) {
	var out map[string]any
	if err := p.getJSON(ctx, endpoint, access, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *Provider) getJSON(ctx context.Context, u, bearer string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.cfg.UserAgent)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := p.cfg.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "application/jwt" {
		return fmt.Errorf("%s answers with a signed JWT; set the client's userinfo_signed_response_alg to none", u)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

// verify checks the ID token's signature, issuer, audience, times and nonce.
func (p *Provider) verify(ctx context.Context, raw, nonce string) (map[string]any, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT")
	}
	var head struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &head); err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("bad signature encoding")
	}
	key, err := p.key(ctx, head.Kid)
	if err != nil {
		return nil, err
	}
	if err := checkSignature(head.Alg, key, []byte(parts[0]+"."+parts[1]), sig); err != nil {
		return nil, err
	}
	var c map[string]any
	dec := json.NewDecoder(bytes.NewReader(mustSegment(parts[1])))
	dec.UseNumber()
	if err := dec.Decode(&c); err != nil {
		return nil, errors.New("bad claims")
	}
	if iss, _ := c["iss"].(string); strings.TrimRight(iss, "/") != p.cfg.Issuer {
		return nil, fmt.Errorf("issued by %q", iss)
	}
	if !audience(c["aud"], p.cfg.ClientID) {
		return nil, errors.New("not issued for this client")
	}
	now := p.cfg.Now()
	const skew = time.Minute
	exp, ok := unix(c["exp"])
	if !ok || now.After(exp.Add(skew)) {
		return nil, errors.New("expired")
	}
	if iat, ok := unix(c["iat"]); ok && iat.After(now.Add(skew)) {
		return nil, errors.New("issued in the future")
	}
	if n, _ := c["nonce"].(string); n != nonce {
		return nil, errors.New("nonce does not match")
	}
	if sub, _ := c["sub"].(string); sub == "" {
		return nil, errors.New("no subject")
	}
	return c, nil
}

func audience(aud any, client string) bool {
	switch a := aud.(type) {
	case string:
		return a == client
	case []any:
		for _, v := range a {
			if s, _ := v.(string); s == client {
				return true
			}
		}
	}
	return false
}

func unix(v any) (time.Time, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return time.Time{}, false
	}
	f, err := n.Float64()
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

func checkSignature(alg string, key crypto.PublicKey, signed, sig []byte) error {
	var h crypto.Hash
	switch alg {
	case "RS256", "ES256":
		h = crypto.SHA256
	case "RS384", "ES384":
		h = crypto.SHA384
	case "RS512", "ES512":
		h = crypto.SHA512
	default:
		return fmt.Errorf("signature algorithm %q is not accepted", alg)
	}
	hh := h.New()
	hh.Write(signed)
	digest := hh.Sum(nil)
	switch k := key.(type) {
	case *rsa.PublicKey:
		if !strings.HasPrefix(alg, "RS") {
			return errors.New("key and algorithm do not match")
		}
		if rsa.VerifyPKCS1v15(k, h, digest, sig) != nil {
			return errors.New("bad signature")
		}
	case *ecdsa.PublicKey:
		if !strings.HasPrefix(alg, "ES") || len(sig)%2 != 0 {
			return errors.New("key and algorithm do not match")
		}
		r, s := new(big.Int).SetBytes(sig[:len(sig)/2]), new(big.Int).SetBytes(sig[len(sig)/2:])
		if !ecdsa.Verify(k, digest, r, s) {
			return errors.New("bad signature")
		}
	default:
		return errors.New("unsupported key")
	}
	return nil
}

// key returns the signing key with this id, fetching the keys again once
// when it is unknown.
func (p *Provider) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	p.mu.Lock()
	k, ok := p.keys[kid]
	p.mu.Unlock()
	if ok {
		return k, nil
	}
	meta, err := p.metadata(ctx)
	if err != nil {
		return nil, err
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := p.getJSON(ctx, meta.JWKS, "", &set); err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	keys := map[string]crypto.PublicKey{}
	for _, j := range set.Keys {
		if j.Use != "" && j.Use != "sig" {
			continue
		}
		if pk, err := j.public(); err == nil {
			keys[j.Kid] = pk
		}
	}
	p.mu.Lock()
	p.keys = keys
	p.mu.Unlock()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	if kid == "" && len(keys) == 1 {
		for _, k := range keys {
			return k, nil
		}
	}
	return nil, fmt.Errorf("no key %q at the provider", kid)
}

type jwk struct {
	Kty, Kid, Use, Crv string
	N, E, X, Y         string
}

func (j jwk) public() (crypto.PublicKey, error) {
	b := func(s string) *big.Int {
		v, _ := base64.RawURLEncoding.DecodeString(s)
		return new(big.Int).SetBytes(v)
	}
	switch j.Kty {
	case "RSA":
		e := b(j.E)
		if j.N == "" || !e.IsInt64() || e.Int64() < 3 {
			return nil, errors.New("bad RSA key")
		}
		return &rsa.PublicKey{N: b(j.N), E: int(e.Int64())}, nil
	case "EC":
		var c elliptic.Curve
		switch j.Crv {
		case "P-256":
			c = elliptic.P256()
		case "P-384":
			c = elliptic.P384()
		case "P-521":
			c = elliptic.P521()
		default:
			return nil, errors.New("unsupported curve")
		}
		k := &ecdsa.PublicKey{Curve: c, X: b(j.X), Y: b(j.Y)}
		if !c.IsOnCurve(k.X, k.Y) { //nolint:staticcheck // the point is checked, nothing else is done with it
			return nil, errors.New("point not on the curve")
		}
		return k, nil
	}
	return nil, errors.New("unsupported key type")
}

func decodeSegment(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return errors.New("bad JWT segment")
	}
	return json.Unmarshal(b, v)
}

func mustSegment(s string) []byte {
	b, _ := base64.RawURLEncoding.DecodeString(s)
	return b
}

func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
