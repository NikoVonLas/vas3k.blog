package blog

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Config struct{ BaseURL, Title, Author, Language, Listen, SMTPAddr, SMTPUser, SMTPPassword, MailFrom string }
type Server struct {
	Store     *Store
	Config    Config
	md        *Markdown
	templates *template.Template
	secret    string
	mux       *http.ServeMux
}
type identityKey struct{}
type commentView struct {
	Comment
	HTML template.HTML
}
type page struct {
	Title, Mode, Message, CSRF, Lang, Query, Block, ReplyTo string
	Site                                                    Config
	User                                                    *User
	Post                                                    Post
	Posts                                                   []Post
	Comments                                                []commentView
	HTML                                                    template.HTML
	StructuredData                                          template.JS
	Kinds                                                   []string
	Page, Next, Previous                                    int
	Translations                                            []Post
	Home                                                    bool
}

func NewServer(ctx context.Context, s *Store, c Config) (*Server, error) {
	u, e := url.Parse(c.BaseURL)
	if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return nil, errors.New("BASE_URL must be an absolute origin without a path")
	}
	c.BaseURL = strings.TrimSuffix(c.BaseURL, "/")
	if c.Language == "" {
		c.Language = "ru"
	}
	if !langPattern.MatchString(c.Language) {
		return nil, errors.New("invalid language")
	}
	if c.Title == "" {
		c.Title = "Личный блог"
	}
	if c.Author == "" {
		c.Author = "Автор"
	}
	_, e = s.DB.Exec(ctx, `INSERT INTO settings(key,value) VALUES('csrf_secret',$1) ON CONFLICT DO NOTHING`, randomToken())
	if e != nil {
		return nil, e
	}
	app := &Server{Store: s, Config: c, md: NewMarkdown(), mux: http.NewServeMux()}
	if e = s.DB.QueryRow(ctx, `SELECT value FROM settings WHERE key='csrf_secret'`).Scan(&app.secret); e != nil {
		return nil, e
	}
	funcs := template.FuncMap{"date": func(t time.Time) string { return t.Format("02.01.2006") }, "iso": func(t time.Time) string { return t.UTC().Format(time.RFC3339) }, "inputDate": func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04") }, "label": kindLabel}
	app.templates, e = template.New("site").Funcs(funcs).ParseFS(assets, "templates/*.html")
	if e != nil {
		return nil, e
	}
	app.routes()
	return app, nil
}
func kindLabel(s string) string {
	m := map[string]string{"blog": "Статьи", "notes": "Заметки", "world": "Путешествия", "gallery": "Фотографии", "inside": "Личное", "365": "Дневник", "reply": "Ответы", "bookmark": "Закладки", "repost": "Репосты"}
	if v, ok := m[s]; ok {
		return v
	}
	return s
}
func (a *Server) routes() {
	m := a.mux
	handle := func(pattern string, h http.HandlerFunc) {
		if strings.HasSuffix(pattern, "/") {
			pattern += "{$}"
		}
		m.HandleFunc(pattern, h)
	}
	static, _ := fs.Sub(assets, "static")
	// Exact asset routes cannot collide with legacy /{kind}/{slug}/index.html.
	fileServer := http.StripPrefix("/static/", http.FileServerFS(static))
	_ = fs.WalkDir(static, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			m.Handle("GET /static/"+path, fileServer)
		}
		return nil
	})

	handle("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if e := a.Store.DB.Ping(r.Context()); e != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	handle("GET /{$}", a.list)
	handle("GET /all/", a.list)
	handle("GET /search/", a.list)
	handle("GET /login/", a.login)
	handle("POST /login/", a.login)
	handle("POST /logout/", a.logout)
	handle("GET /profile/", a.profile)
	handle("POST /profile/", a.profile)
	handle("GET /godmode/", a.admin)
	handle("GET /godmode/posts/new/", a.edit)
	handle("POST /godmode/posts/new/", a.edit)
	handle("GET /godmode/posts/{id}/", a.edit)
	handle("POST /godmode/posts/{id}/", a.edit)
	handle("POST /godmode/users/", a.createUser)
	handle("GET /godmode/export/", a.export)
	handle("POST /godmode/posts/{id}/announce/", a.announce)
	handle("POST /comments/create/", a.comment)
	handle("POST /comments/{id}/delete/", a.moderate)
	handle("POST /comments/{id}/pin/", a.moderate)
	handle("POST /clickers/comments/{id}/", a.react)
	handle("POST /clickers/blocks/{slug}/{block}/", a.react)
	handle("GET /rss/", a.feed)
	handle("GET /rss/{scope}/", a.feed)
	handle("GET /feed.json", a.jsonFeed)
	handle("GET /llms.txt", a.llms)
	handle("GET /{kind}/{slug}/index.md", a.markdownPost)
	handle("GET /sitemap.xml", a.sitemap)
	handle("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "User-agent: *\nDisallow: /godmode/\nDisallow: /profile/\nSitemap: %s/sitemap.xml\n", a.Config.BaseURL)
	})
	handle("GET /subscribe/", a.subscribe)
	handle("POST /subscribe/", a.subscribe)
	handle("GET /subscribe/confirm/{token}/", a.confirm)
	handle("GET /unsubscribe/{token}/", a.unsubscribe)
	handle("POST /unsubscribe/{token}/", a.unsubscribe)
	handle("GET /donate/", func(w http.ResponseWriter, r *http.Request) {
		a.render(w, r, page{Mode: "message", Title: "Поддержать блог", Message: "Контакты автора — на главной странице."})
	})
	handle("GET /post-style/{id}", a.postStyle)
	handle("GET /{kind}/", a.list)
	handle("GET /{kind}/{slug}/", a.show)
	handle("GET /{kind}/{slug}/index.html", a.show)
	handle("GET /{kind}/{slug}/{block}/", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("block") == "edit" {
			a.legacyEdit(w, r)
			return
		}
		p, e := a.Store.Post(r.Context(), r.PathValue("slug"), a.lang(r))
		if e != nil || !p.Readable(currentUser(r)) {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, p.URL()+"#"+url.PathEscape(r.PathValue("block")), http.StatusMovedPermanently)
	})
}
func (a *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https: http:; media-src 'self' https:; style-src 'self'; script-src 'self'; frame-src https://www.youtube-nocookie.com; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Language", a.lang(r))
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	if c, e := r.Cookie("session"); e == nil {
		u, e := a.Store.Session(r.Context(), c.Value)
		if e == nil {
			r = r.WithContext(context.WithValue(r.Context(), identityKey{}, u))
		} else if !errors.Is(e, pgx.ErrNoRows) {
			a.fail(w, e)
			return
		}
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
		if !a.validCSRF(r) {
			http.Error(w, "Обновите страницу и повторите действие", http.StatusForbidden)
			return
		}
	}
	a.mux.ServeHTTP(w, r)
}
func currentUser(r *http.Request) *User { u, _ := r.Context().Value(identityKey{}).(*User); return u }
func (a *Server) lang(r *http.Request) string {
	l := r.URL.Query().Get("lang")
	if langPattern.MatchString(l) {
		return l
	}
	return a.Config.Language
}
func (a *Server) sign(s string) string {
	h := hmac.New(sha256.New, []byte(a.secret))
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}
func (a *Server) csrf(w http.ResponseWriter, r *http.Request) string {
	if c, e := r.Cookie("csrf"); e == nil && a.validToken(c.Value) {
		return c.Value
	}
	nonce := randomToken()
	token := nonce + "." + a.sign(nonce)
	a.cookie(w, "csrf", token, 86400)
	return token
}
func (a *Server) validToken(t string) bool {
	p := strings.Split(t, ".")
	return len(p) == 2 && len(p[0]) == 64 && hmac.Equal([]byte(p[1]), []byte(a.sign(p[0])))
}
func (a *Server) validCSRF(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		if u, e := url.Parse(r.Referer()); e == nil && u.Host != "" {
			origin = u.Scheme + "://" + u.Host
		}
	}
	if origin != a.Config.BaseURL {
		return false
	}
	if e := r.ParseForm(); e != nil {
		return false
	}
	c, e := r.Cookie("csrf")
	if e != nil || !a.validToken(c.Value) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.PostForm.Get("csrf"))) == 1
}
func (a *Server) cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: age, HttpOnly: true, Secure: strings.HasPrefix(a.Config.BaseURL, "https://"), SameSite: http.SameSiteLaxMode})
}
func (a *Server) render(w http.ResponseWriter, r *http.Request, p page) {
	p.Site = a.Config
	p.User = currentUser(r)
	p.CSRF = a.csrf(w, r)
	p.Lang = a.lang(r)
	p.Kinds = Kinds
	if p.Mode == "post" {
		p.StructuredData = a.structuredPost(p.Post)
		w.Header().Set("Link", fmt.Sprintf(`<%s%s>; rel="alternate"; type="text/markdown", <%s/llms.txt?lang=%s>; rel="describedby"`, a.Config.BaseURL, p.Post.MarkdownURL(), a.Config.BaseURL, p.Post.Lang))
	}
	var b bytes.Buffer
	if e := a.templates.ExecuteTemplate(&b, "layout", p); e != nil {
		a.fail(w, e)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b.Bytes())
}
func (a *Server) fail(w http.ResponseWriter, e error) {
	slog.Error("request failed", "type", fmt.Sprintf("%T", e))
	http.Error(w, "Не удалось выполнить запрос", 500)
}
func (a *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	u := currentUser(r)
	if u == nil {
		http.Redirect(w, r, "/login/", 303)
		return false
	}
	if !u.Admin {
		http.Error(w, "Недостаточно прав", 403)
		return false
	}
	return true
}
func (a *Server) limited(w http.ResponseWriter, r *http.Request, key string, n int, d time.Duration) bool {
	ip, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		ip = r.RemoteAddr
	}
	ok, e := a.Store.Allow(r.Context(), key+":"+a.sign(ip), n, d)
	if e != nil {
		a.fail(w, e)
		return true
	}
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(d.Seconds())))
		http.Error(w, "Слишком много запросов. Попробуйте позже.", 429)
		return true
	}
	return false
}
func (a *Server) list(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != "" {
		found := false
		for _, k := range Kinds {
			if kind == k {
				found = true
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if n < 1 {
		n = 1
	}
	if n > 100000 {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query().Get("q")
	if len(q) > 300 {
		http.Error(w, "Слишком длинный запрос", 400)
		return
	}
	// Search never examines protected text, even if only its title would be returned.
	posts, e := a.Store.Posts(r.Context(), Filter{Lang: a.lang(r), Kind: kind, Search: q, PublicOnly: q != "", Home: r.URL.Path == "/", Limit: 31, Offset: (n - 1) * 30})
	if e != nil {
		a.fail(w, e)
		return
	}
	p := page{Mode: "list", Title: kindLabel(kind), Posts: posts, Page: n, Query: q}
	if r.URL.Path == "/" {
		p.Title = a.Config.Title
		p.Home = q == ""
	}
	if q != "" {
		p.Title = "Поиск: " + q
	}
	if len(posts) > 30 {
		p.Next = n + 1
		p.Posts = posts[:30]
	}
	if n > 1 {
		p.Previous = n - 1
	}
	a.render(w, r, p)
}
func (a *Server) show(w http.ResponseWriter, r *http.Request) {
	p, e := a.Store.Post(r.Context(), r.PathValue("slug"), a.lang(r))
	if errors.Is(e, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if e != nil {
		a.fail(w, e)
		return
	}
	u := currentUser(r)
	if !p.Public(time.Now()) && (u == nil || !u.Admin) {
		http.NotFound(w, r)
		return
	}
	if p.Kind != r.PathValue("kind") || strings.HasSuffix(r.URL.Path, "index.html") {
		http.Redirect(w, r, p.URL(), 301)
		return
	}
	if !p.Readable(u) {
		a.render(w, r, page{Title: p.Title, Mode: "locked", Post: p})
		return
	}
	if p.ExternalURL != "" {
		http.Redirect(w, r, p.ExternalURL, 302)
		return
	}
	if _, e = a.Store.DB.Exec(r.Context(), `UPDATE posts SET views=views+1 WHERE id=$1`, p.ID); e != nil {
		a.fail(w, e)
		return
	}
	p.Views++
	cs, e := a.Store.Comments(r.Context(), p.ID)
	if e != nil {
		a.fail(w, e)
		return
	}
	cv := []commentView{}
	for _, c := range cs {
		cv = append(cv, commentView{c, a.md.Comment(c.Body)})
	}
	body := strings.ReplaceAll(string(a.md.Render(p.Body, p.RawHTML)), `href="?block=`, `href="?lang=`+url.QueryEscape(p.Lang)+`&amp;block=`)
	if u == nil {
		body = strings.ReplaceAll(body, "#comment-form", "#comments")
	}
	pageData := page{Title: p.Title, Mode: "post", Post: p, HTML: template.HTML(body), Comments: cv, Block: r.URL.Query().Get("block"), ReplyTo: r.URL.Query().Get("reply")}
	rows, e := a.Store.DB.Query(r.Context(), `SELECT `+postCols+` FROM posts WHERE slug=$1 AND lang<>$2 AND visible AND published_at<=now()`, p.Slug, p.Lang)
	if e != nil {
		a.fail(w, e)
		return
	}
	defer rows.Close()
	for rows.Next() {
		other, e := scanPost(rows)
		if e != nil {
			a.fail(w, e)
			return
		}
		pageData.Translations = append(pageData.Translations, other)
	}
	if e = rows.Err(); e != nil {
		a.fail(w, e)
		return
	}
	a.render(w, r, pageData)
}
func (a *Server) postStyle(w http.ResponseWriter, r *http.Request) {
	p, e := a.Store.PostID(r.Context(), r.PathValue("id"))
	if e != nil || !p.Readable(currentUser(r)) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	fmt.Fprint(w, p.CSS)
}
func (a *Server) legacyEdit(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	p, e := a.Store.Post(r.Context(), r.PathValue("slug"), a.lang(r))
	if e != nil {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/godmode/posts/"+p.ID+"/", 302)
}
