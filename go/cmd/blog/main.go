package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/NikoVonLas/vas3k.blog/go/internal/blog"
)

func main() {
	if e := run(); e != nil {
		slog.Error("blog stopped", "error", e)
		os.Exit(1)
	}
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	store, e := blog.Open(ctx, dsn)
	if e != nil {
		return e
	}
	defer store.DB.Close()
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	switch command {
	case "migrate":
		return store.Migrate(ctx)
	case "create-owner":
		email := strings.ToLower(strings.TrimSpace(os.Getenv("OWNER_EMAIL")))
		addr, e := mail.ParseAddress(email)
		if e != nil || addr.Address != email {
			return errors.New("valid OWNER_EMAIL is required")
		}
		// Read the password through stdin; never put it into argv or logs.
		password, e := bufio.NewReader(os.Stdin).ReadString('\n')
		if e != nil && password == "" {
			return errors.New("read owner password from stdin")
		}
		if e = store.CreateUser(ctx, email, env("OWNER_NAME", "Автор"), strings.TrimRight(password, "\r\n"), true); e != nil {
			return errors.New("cannot create owner: check email uniqueness and password length (12–72 bytes)")
		}
		fmt.Println("Owner created")
		return nil
	case "import":
		var posts []blog.Post
		dec := json.NewDecoder(os.Stdin)
		if e = dec.Decode(&posts); e != nil {
			return e
		}
		for _, p := range posts {
			if !p.Valid() {
				return fmt.Errorf("invalid post %q", p.Slug)
			}
		}
		if e = store.ImportPosts(ctx, posts); e != nil {
			return errors.New("import failed and rolled back; check duplicate addresses and field values")
		}
		fmt.Printf("Imported %d posts\n", len(posts))
		return nil
	case "serve":
	default:
		return errors.New("usage: blog [serve|migrate|create-owner|import]")
	}
	cfg := blog.Config{BaseURL: env("BASE_URL", "http://localhost:8080"), Listen: env("LISTEN_ADDR", "127.0.0.1:8080"), Title: env("BLOG_TITLE", "Личный блог"), Author: env("BLOG_AUTHOR", "Автор"), Language: env("BLOG_LANGUAGE", "ru"), SMTPAddr: os.Getenv("SMTP_ADDR"), SMTPUser: os.Getenv("SMTP_USER"), SMTPPassword: os.Getenv("SMTP_PASSWORD"), MailFrom: os.Getenv("MAIL_FROM")}
	app, e := blog.NewServer(ctx, store, cfg)
	if e != nil {
		return e
	}
	server := &http.Server{Addr: cfg.Listen, Handler: app, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 16}
	go app.RunMailWorker(ctx)
	go func() {
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(stop)
	}()
	slog.Info("blog listening", "address", cfg.Listen)
	e = server.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
