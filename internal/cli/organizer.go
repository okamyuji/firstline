package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"

	"github.com/okamyuji/firstline/internal/app"
	"github.com/okamyuji/firstline/internal/domain"
)

const (
	// 1024 runes are at most 4 KiB of UTF-8.
	maxPasswordLine = 4 << 10
	// json.Marshal escapes some runes to six bytes, so the body holds six
	// times the longest password plus the email.
	maxOrganizerBody = 16 << 10
)

type organizerInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type organizerCreator interface {
	CreateOrganizer(ctx context.Context, email, password string) error
}

// organizerHandler is mounted only on the admin socket's server, so the
// public and admin TCP listeners answer 404 for /organizers.
func organizerHandler(c organizerCreator, check checkFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /organizers", func(w http.ResponseWriter, r *http.Request) {
		var in organizerInput
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOrganizerBody)).Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		err := c.CreateOrganizer(r.Context(), in.Email, in.Password)
		code := organizerStatus(err)
		if code == http.StatusInternalServerError {
			check(r.Context(), "create organizer", err)
		}
		w.WriteHeader(code)
	})
	return withDeadline(mux)
}

// organizerStatus uses if statements, not a tagless switch: Go's coverage
// blocks leave case expressions out, so their mutants would count as uncovered.
func organizerStatus(err error) int {
	if err == nil {
		return http.StatusCreated
	}
	if errors.Is(err, domain.ErrBadInput) {
		return http.StatusBadRequest
	}
	if errors.Is(err, domain.ErrDuplicateOrganizer) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func createOrganizerCmd(ctx context.Context, e env, cfg Config, args []string) int {
	fs := flag.NewFlagSet("create-organizer", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	email := fs.String("email", "", "organizer email address")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	pw, err := readPassword(e.stdin)
	if err == nil {
		err = app.CheckOrganizerInput(*email, pw)
	}
	if err != nil {
		return exitCode(e.stderr, "create-organizer", err)
	}
	status, err := postOrganizer(ctx, cfg.AdminSock, *email, pw)
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		// No serve is listening, so write under the lock like serve would.
		return exitCode(e.stderr, "create-organizer", createDirect(ctx, cfg, *email, pw, e))
	}
	return exitCode(e.stderr, "create-organizer", checkCreated(status, err))
}

// checkCreated turns serve's answer into an error unless it is 201.
func checkCreated(status int, err error) error {
	if err == nil && status != http.StatusCreated {
		return fmt.Errorf("serve answered %d %s", status, http.StatusText(status))
	}
	return err
}

// readPassword reads the first line of r, which may end at EOF, and refuses a
// line longer than maxPasswordLine without reading the rest.
func readPassword(r io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(r, int64(maxPasswordLine+len("\r\n")))).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) > maxPasswordLine {
		return "", domain.ErrBadInput
	}
	return line, nil
}

func postOrganizer(ctx context.Context, sock, email, pw string) (int, error) {
	body, err := json.Marshal(organizerInput{Email: email, Password: pw})
	if err != nil {
		return 0, err
	}
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	// The host is a placeholder: the transport always dials the socket.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://firstline/organizers", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

func createDirect(ctx context.Context, cfg Config, email, pw string, e env) error {
	d, err := openDirect(ctx, cfg, nil, newReporter(cfg.ErrorReporters, e.log), e.log)
	if err != nil {
		return err
	}
	return errors.Join(d.svc.CreateOrganizer(ctx, email, pw), d.close())
}
