package blog

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

func (b *browser) passkeyJSON(path string, input any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(input)
	r := httptest.NewRequest("POST", b.app.Config.BaseURL+path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", b.app.Config.BaseURL)
	for _, c := range b.cookies {
		r.AddCookie(c)
	}
	if c := b.cookies["csrf"]; c != nil {
		r.Header.Set("X-CSRF-Token", c.Value)
	}
	w := httptest.NewRecorder()
	b.app.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	return w
}

type virtualPasskey struct {
	key      *ecdsa.PrivateKey
	id, user []byte
	counter  uint32
}

func newVirtualPasskey(t *testing.T) *virtualPasskey {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	id := make([]byte, 32)
	rand.Read(id)
	return &virtualPasskey{key: key, id: id}
}
func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func (v *virtualPasskey) response(t *testing.T, w *httptest.ResponseRecorder, register bool, origin, rpID string, uv bool) map[string]any {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("begin: %d %s", w.Code, w.Body.String())
	}
	var options struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			User      struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &options); e != nil || options.PublicKey.Challenge == "" {
		t.Fatal("bad challenge")
	}
	typ := "webauthn.get"
	if register {
		typ = "webauthn.create"
		v.user, _ = base64.RawURLEncoding.DecodeString(options.PublicKey.User.ID)
	}
	client, _ := json.Marshal(map[string]any{"type": typ, "challenge": options.PublicKey.Challenge, "origin": origin, "crossOrigin": false})
	rpHash := sha256.Sum256([]byte(rpID))
	auth := append([]byte{}, rpHash[:]...)
	flags := byte(1)
	if uv {
		flags |= 4
	}
	if register {
		flags |= 64
	}
	auth = append(auth, flags)
	v.counter++
	auth = binary.BigEndian.AppendUint32(auth, v.counter)
	response := map[string]any{"clientDataJSON": b64(client)}
	if register {
		public, e := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: v.key.X.FillBytes(make([]byte, 32)), -3: v.key.Y.FillBytes(make([]byte, 32))})
		if e != nil {
			t.Fatal(e)
		}
		auth = append(auth, make([]byte, 16)...)
		auth = binary.BigEndian.AppendUint16(auth, uint16(len(v.id)))
		auth = append(auth, v.id...)
		auth = append(auth, public...)
		attestation, e := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
		if e != nil {
			t.Fatal(e)
		}
		response["attestationObject"] = b64(attestation)
		response["transports"] = []string{"internal"}
	} else {
		clientHash := sha256.Sum256(client)
		signed := append(append([]byte{}, auth...), clientHash[:]...)
		digest := sha256.Sum256(signed)
		signature, e := ecdsa.SignASN1(rand.Reader, v.key, digest[:])
		if e != nil {
			t.Fatal(e)
		}
		response["authenticatorData"] = b64(auth)
		response["signature"] = b64(signature)
		response["userHandle"] = b64(v.user)
	}
	return map[string]any{"id": b64(v.id), "rawId": b64(v.id), "type": "public-key", "response": response, "clientExtensionResults": map[string]any{}}
}
func enrollTestPasskey(t *testing.T, a *Server, b *browser, v *virtualPasskey) string {
	t.Helper()
	token, e := a.Store.PrepareAdminPasskey(context.Background(), "owner@example.test", "Owner")
	if e != nil {
		t.Fatal(e)
	}
	b.request("GET", "/admin/setup", nil)
	begin := b.passkeyJSON("/admin/passkey/register/begin", map[string]string{"token": token, "label": "Test key"})
	response := v.response(t, begin, true, a.Config.BaseURL, "blog.test", true)
	if w := b.passkeyJSON("/admin/passkey/register/finish", response); w.Code != 200 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	return token
}
func authenticateTestPasskey(t *testing.T, a *Server, b *browser, v *virtualPasskey) {
	t.Helper()
	b.request("GET", "/admin", nil)
	begin := b.passkeyJSON("/admin/passkey/login/begin", map[string]string{"next": "/godmode/"})
	response := v.response(t, begin, false, a.Config.BaseURL, "blog.test", true)
	if w := b.passkeyJSON("/admin/passkey/login/finish", response); w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	b.request("GET", "/admin/keys", nil)
}
func TestPasskeyLifecycle(t *testing.T) {
	a := testApp(t)
	b := newBrowser(a)
	v := newVirtualPasskey(t)
	login := b.request("GET", "/login/", nil).Body.String()
	if strings.Contains(login, `name="password"`) || strings.Contains(login, "Вход по почте") || strings.Contains(login, "/admin") {
		t.Fatal("public login exposes admin/password")
	}
	if w := b.request("POST", "/login/", url.Values{"email": {"owner@example.test"}, "password": {"test-only-password"}}); w.Code != 405 {
		t.Fatalf("password endpoint exists: %d", w.Code)
	}
	token := enrollTestPasskey(t, a, b, v)
	if b.cookies["session"] != nil {
		t.Fatal("setup grant issued session without key login")
	}
	if w := b.passkeyJSON("/admin/passkey/register/begin", map[string]string{"token": token}); w.Code != 403 {
		t.Fatal("reused setup grant")
	}
	if w := b.request("GET", "/godmode/", nil); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/admin?") {
		t.Fatal("admin not protected")
	}
	authenticateTestPasskey(t, a, b, v)
	u, err := a.Store.Session(context.Background(), b.cookies["session"].Value)
	if err != nil || !u.Admin {
		t.Fatal("valid passkey not logged in")
	}
	if w := b.request("GET", "/godmode/", nil); w.Code != 200 {
		t.Fatal("editor inaccessible")
	}
	if w := b.request("POST", "/admin/keys/"+b64(v.id)+"/delete", nil); w.Code != 400 {
		t.Fatal("removed final key")
	}
	// Add a spare using a recent Passkey session, with no setup grant.
	spare := newVirtualPasskey(t)
	begin := b.passkeyJSON("/admin/passkey/register/begin", map[string]string{"label": "Spare"})
	if w := b.passkeyJSON("/admin/passkey/register/finish", spare.response(t, begin, true, a.Config.BaseURL, "blog.test", true)); w.Code != 200 {
		t.Fatalf("spare: %d %s", w.Code, w.Body.String())
	}
	second := newBrowser(a)
	authenticateTestPasskey(t, a, second, spare)
	oldSession := b.cookies["session"].Value
	if w := second.request("POST", "/admin/keys/"+b64(v.id)+"/delete", nil); w.Code != 303 {
		t.Fatal("key deletion failed")
	}
	if _, e := a.Store.Session(context.Background(), oldSession); e == nil {
		t.Fatal("removed key left session alive")
	}
	// Normal logout revokes the current session.
	current := second.cookies["session"].Value
	second.request("POST", "/logout/", nil)
	if _, e := a.Store.Session(context.Background(), current); e == nil {
		t.Fatal("logout left session alive")
	}
}
func TestPasskeyAssertionSecurity(t *testing.T) {
	a := testApp(t)
	b := newBrowser(a)
	v := newVirtualPasskey(t)
	enrollTestPasskey(t, a, b, v)
	for _, kind := range []string{"origin", "rpID", "no verification", "signature", "challenge", "user handle", "expired", "other browser", "success and replay"} {
		t.Run(kind, func(t *testing.T) {
			b.request("GET", "/admin", nil)
			begin := b.passkeyJSON("/admin/passkey/login/begin", map[string]string{"next": "//evil.example/"})
			cookie := b.cookies["passkey_flow"]
			origin, rpID, uv := a.Config.BaseURL, "blog.test", true
			switch kind {
			case "origin":
				origin = "https://evil.example"
			case "rpID":
				rpID = "evil.example"
			case "no verification":
				uv = false
			}
			response := v.response(t, begin, false, origin, rpID, uv)
			data := response["response"].(map[string]any)
			switch kind {
			case "signature":
				data["signature"] = b64([]byte("wrong"))
			case "challenge":
				raw, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": "wrong", "origin": origin})
				data["clientDataJSON"] = b64(raw)
			case "user handle":
				data["userHandle"] = b64([]byte("someone-else"))
			case "expired":
				a.Store.DB.Exec(context.Background(), `UPDATE passkey_ceremonies SET expires_at=now()-interval '1 second'`)
			case "other browser":
				delete(b.cookies, "passkey_flow")
			}
			w := b.passkeyJSON("/admin/passkey/login/finish", response)
			if kind == "success and replay" {
				if w.Code != 200 || !strings.Contains(w.Body.String(), `"redirect":"/godmode/"`) {
					t.Fatalf("valid login %d %s", w.Code, w.Body.String())
				}
				b.cookies["passkey_flow"] = cookie
				b.request("GET", "/admin/keys", nil)
				if w = b.passkeyJSON("/admin/passkey/login/finish", response); w.Code != 400 {
					t.Fatal("replayed assertion")
				}
			} else if w.Code != 400 && w.Code != 401 {
				t.Fatalf("accepted %s: %d", kind, w.Code)
			}
		})
	}
}
func TestPasskeyEnrollmentSecurity(t *testing.T) {
	a := testApp(t)
	b := newBrowser(a)
	b.request("GET", "/admin/setup", nil)
	for _, token := range []string{"", randomToken()} {
		if w := b.passkeyJSON("/admin/passkey/register/begin", map[string]string{"token": token}); w.Code != 403 {
			t.Fatal("public enrollment")
		}
	}
	token, err := a.Store.PrepareAdminPasskey(context.Background(), "owner@example.test", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"origin", "rpID", "no verification", "expired grant", "replaced grant"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "expired grant" || kind == "replaced grant" {
				token, err = a.Store.PrepareAdminPasskey(context.Background(), "owner@example.test", "Owner")
				if err != nil {
					t.Fatal(err)
				}
			}
			v := newVirtualPasskey(t)
			begin := b.passkeyJSON("/admin/passkey/register/begin", map[string]string{"token": token})
			origin, rpID, uv := a.Config.BaseURL, "blog.test", true
			switch kind {
			case "origin":
				origin = "https://evil.example"
			case "rpID":
				rpID = "evil.example"
			case "no verification":
				uv = false
			}
			response := v.response(t, begin, true, origin, rpID, uv)
			if kind == "expired grant" {
				a.Store.DB.Exec(context.Background(), `UPDATE admin_setup_grants SET expires_at=now()-interval '1 second'`)
			}
			if kind == "replaced grant" {
				a.Store.PrepareAdminPasskey(context.Background(), "owner@example.test", "Owner")
			}
			w := b.passkeyJSON("/admin/passkey/register/finish", response)
			if w.Code != 400 && w.Code != 403 {
				t.Fatalf("accepted bad registration: %d %s", w.Code, w.Body.String())
			}
		})
	}
	var count int
	a.Store.DB.QueryRow(context.Background(), `SELECT count(*) FROM admin_passkeys`).Scan(&count)
	if count != 0 {
		t.Fatal("unverified key saved")
	}
	if _, err := a.Store.PrepareAdminPasskey(context.Background(), "member@example.test", "Member"); err == nil {
		t.Fatal("member silently promoted")
	}
}
func TestAdminOldSessionsAndCSRF(t *testing.T) {
	a := testApp(t)
	b := newBrowser(a)
	u, _ := a.Store.UserEmail(context.Background(), "owner@example.test")
	token := randomToken()
	a.Store.DB.Exec(context.Background(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 day')`, hash(token), u.ID)
	if _, err := a.Store.Session(context.Background(), token); err == nil {
		t.Fatal("password session still authorizes admin")
	}
	for _, path := range []string{"login/begin", "login/finish", "register/begin", "register/finish"} {
		w := apiRequest(a, "POST", "/admin/passkey/"+path, "application/json", `{}`, "")
		if w.Code != 403 {
			t.Fatalf("missing CSRF accepted: %s %d", path, w.Code)
		}
	}
	v := newVirtualPasskey(t)
	enrollTestPasskey(t, a, b, v)
	authenticateTestPasskey(t, a, b, v)
	a.Store.DB.Exec(context.Background(), `UPDATE sessions SET created_at=now()-interval '11 minutes'`)
	if w := b.passkeyJSON("/admin/passkey/register/begin", map[string]string{}); w.Code != 403 {
		t.Fatal("stale session adds keys")
	}
	a.Store.DB.Exec(context.Background(), `UPDATE users SET is_banned=true WHERE id=$1`, u.ID)
	if _, err := a.Store.Session(context.Background(), b.cookies["session"].Value); err == nil {
		t.Fatal("banned session alive")
	}
	b.request("GET", "/admin", nil)
	begin := b.passkeyJSON("/admin/passkey/login/begin", map[string]string{})
	if w := b.passkeyJSON("/admin/passkey/login/finish", v.response(t, begin, false, a.Config.BaseURL, "blog.test", true)); w.Code != 401 {
		t.Fatal("banned admin authenticated")
	}
}
