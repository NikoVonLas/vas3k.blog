package blog

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

func (a *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		a.render(w, r, page{Mode: "login", Title: "Войти"})
		return
	}
	if a.limited(w, r, "login", 10, 15*time.Minute) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PostForm.Get("email")))
	password := r.PostForm.Get("password")
	u, e := a.Store.UserEmail(r.Context(), email)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		a.fail(w, e)
		return
	}
	// A fixed valid bcrypt hash keeps the absent-account path computationally comparable.
	h := []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")
	if e == nil {
		h = []byte(u.PasswordHash)
	}
	valid := bcrypt.CompareHashAndPassword(h, []byte(password)) == nil
	if e != nil || !valid || u.Banned {
		a.render(w, r, page{Mode: "login", Title: "Войти", Message: "Неверная почта или пароль"})
		return
	}
	token := randomToken()
	_, e = a.Store.DB.Exec(r.Context(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '30 days')`, hash(token), u.ID)
	if e != nil {
		a.fail(w, e)
		return
	}
	a.cookie(w, "session", token, 30*86400)
	a.cookie(w, "csrf", "", -1)
	http.Redirect(w, r, loginNext(r.URL.Query().Get("next")), 303)
}
func (a *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("session"); e == nil {
		if _, e = a.Store.DB.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, hash(c.Value)); e != nil {
			a.fail(w, e)
			return
		}
	}
	a.cookie(w, "session", "", -1)
	a.cookie(w, "csrf", "", -1)
	http.Redirect(w, r, "/", 303)
}
func (a *Server) profile(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil {
		http.Redirect(w, r, "/login/", 303)
		return
	}
	if r.Method == "POST" {
		name := strings.TrimSpace(r.PostForm.Get("name"))
		avatar := strings.TrimSpace(r.PostForm.Get("avatar"))
		if name == "" || len(name) > 128 || !safeURL(avatar) {
			http.Error(w, "Проверьте имя и адрес аватара", 400)
			return
		}
		if _, e := a.Store.DB.Exec(r.Context(), `UPDATE users SET name=$1,avatar=$2 WHERE id=$3`, name, avatar, u.ID); e != nil {
			a.fail(w, e)
			return
		}
		http.Redirect(w, r, "/profile/", 303)
		return
	}
	a.render(w, r, page{Mode: "profile", Title: "Профиль"})
}
func (a *Server) admin(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	posts, e := a.Store.Posts(r.Context(), Filter{Admin: true, Limit: 1000})
	if e != nil {
		a.fail(w, e)
		return
	}
	a.render(w, r, page{Mode: "admin", Title: "Публикации", Posts: posts})
}
func (a *Server) edit(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	p := Post{Kind: "blog", Lang: a.Config.Language, Author: a.Config.Author, Commentable: true, OnHome: true, PublishedAt: time.Now().UTC(), Data: json.RawMessage(`{}`)}
	if id != "" {
		var e error
		p, e = a.Store.PostID(r.Context(), id)
		if errors.Is(e, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if e != nil {
			a.fail(w, e)
			return
		}
	}
	if r.Method == "POST" {
		f := r.PostForm
		p.Title = f.Get("title")
		p.Slug = f.Get("slug")
		p.Kind = f.Get("kind")
		p.Lang = f.Get("lang")
		p.Subtitle = f.Get("subtitle")
		p.Body = f.Get("body")
		p.Summary = f.Get("summary")
		p.Image = f.Get("image")
		p.Author = f.Get("author")
		p.ExternalURL = f.Get("external_url")
		p.CSS = f.Get("css")
		p.Visible = f.Get("visible") == "on"
		p.MembersOnly = f.Get("members_only") == "on"
		p.Commentable = f.Get("commentable") == "on"
		p.OnHome = f.Get("on_home") == "on"
		p.RawHTML = f.Get("raw_html") == "on"
		t, e := time.Parse("2006-01-02T15:04", f.Get("published_at"))
		p.PublishedAt = t
		if e != nil || !p.Valid() {
			a.render(w, r, page{Mode: "edit", Title: "Редактирование", Post: p, Message: "Проверьте адрес, язык, ссылки и дату публикации (UTC)."})
			return
		}
		if e = a.Store.SavePost(r.Context(), &p); e != nil {
			a.render(w, r, page{Mode: "edit", Title: "Редактирование", Post: p, Message: "Не удалось сохранить. Возможно, этот адрес и язык уже заняты."})
			return
		}
		http.Redirect(w, r, p.URL(), 303)
		return
	}
	a.render(w, r, page{Mode: "edit", Title: "Редактирование", Post: p})
}
func (a *Server) createUser(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PostForm.Get("email")))
	name := strings.TrimSpace(r.PostForm.Get("name"))
	addr, e := mail.ParseAddress(email)
	if e != nil || addr.Address != email || name == "" || len(name) > 128 {
		http.Error(w, "Проверьте почту и имя", 400)
		return
	}
	if e = a.Store.CreateUser(r.Context(), email, name, r.PostForm.Get("password"), false); e != nil {
		http.Error(w, "Не удалось создать пользователя. Пароль: 12–72 байта; почта должна быть уникальной.", 400)
		return
	}
	http.Redirect(w, r, "/godmode/", 303)
}
func (a *Server) export(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	posts := []Post{}
	for offset := 0; ; offset += 1000 {
		batch, e := a.Store.Posts(r.Context(), Filter{Admin: true, Limit: 1000, Offset: offset})
		if e != nil {
			a.fail(w, e)
			return
		}
		posts = append(posts, batch...)
		if len(batch) < 1000 {
			break
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="posts.json"`)
	_ = json.NewEncoder(w).Encode(posts)
}
