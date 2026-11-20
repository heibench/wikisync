package mw

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(srv *httptest.Server) *Client {
	return &Client{HTTP: srv.Client(), APIURL: srv.URL, UserAgent: "ua"}
}

func TestFetchPagesReadsEveryField(t *testing.T) {
	const body = `{"query":{"pages":[{"pageid":5,"ns":2,"title":"User:Water Pump",` +
		`"revisions":[{"revid":99,"slots":{"main":{"content":"hello\n"}}}]}]}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.FormValue("formatversion"); got != "2" {
			t.Errorf("formatversion = %q, want 2", got)
		}
		if got := r.FormValue("rvprop"); got != "ids|content" {
			t.Errorf("rvprop = %q, want ids|content", got)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	got, err := testClient(srv).FetchPages([]string{"User:Water Pump"})
	if err != nil {
		t.Fatal(err)
	}
	want := FetchedPage{Title: "User:Water Pump", Text: "hello\n", RevID: 99, PageID: 5, NS: 2, Exists: true}
	if got["User:Water Pump"] != want {
		t.Errorf("FetchPages = %+v, want %+v", got, want)
	}
}

func TestCurrentRevIDAsksForTheIDOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("rvprop"); got != "ids" {
			t.Errorf("rvprop = %q, want ids", got)
		}
		if r.URL.Query().Get("titles") == "Ghost" {
			_, _ = io.WriteString(w, `{"query":{"pages":[{"title":"Ghost","missing":true}]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"query":{"pages":[{"pageid":5,"title":"A","revisions":[{"revid":99}]}]}}`)
	}))
	defer srv.Close()
	c := testClient(srv)

	if rev, exists, err := c.CurrentRevID("A"); rev != 99 || !exists || err != nil {
		t.Errorf("CurrentRevID(A) = (%d, %v, %v), want (99, true, nil)", rev, exists, err)
	}
	if rev, exists, err := c.CurrentRevID("Ghost"); rev != 0 || exists || err != nil {
		t.Errorf("CurrentRevID(Ghost) = (%d, %v, %v), want (0, false, nil)", rev, exists, err)
	}
}

// fakeWiki routes the login/token/edit calls a push makes, recording the last
// edit it saw.
func TestLoginAndEdit(t *testing.T) {
	var gotText, gotSummary, gotBaseRevID, gotToken string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		action := r.FormValue("action")
		if action == "" {
			action = r.URL.Query().Get("action")
		}
		switch action {
		case "query": // meta=tokens
			typ := r.URL.Query().Get("type")
			_, _ = io.WriteString(w, `{"query":{"tokens":{"`+typ+`token":"TOK-`+typ+`"}}}`)
		case "login":
			if r.FormValue("lgtoken") != "TOK-login" {
				t.Errorf("login token = %q", r.FormValue("lgtoken"))
			}
			_, _ = io.WriteString(w, `{"login":{"result":"Success"}}`)
		case "edit":
			gotText = r.FormValue("text")
			gotSummary = r.FormValue("summary")
			gotBaseRevID = r.FormValue("baserevid")
			gotToken = r.FormValue("token")
			_, _ = io.WriteString(w, `{"edit":{"result":"Success","newrevid":1234}}`)
		default:
			t.Errorf("unexpected action %q", action)
		}
	}))
	defer srv.Close()

	c := testClient(srv)
	if err := c.Login("bot", "pw"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	newRev, err := c.Edit("Water Pump", "new text\n", "tidy", 99)
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if newRev != 1234 {
		t.Errorf("newRev = %d, want 1234", newRev)
	}
	if gotText != "new text\n" || gotSummary != "tidy" || gotBaseRevID != "99" || gotToken != "TOK-csrf" {
		t.Errorf("edit params: text=%q summary=%q baserevid=%q token=%q", gotText, gotSummary, gotBaseRevID, gotToken)
	}
}

func TestEditReportsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Query().Get("action") == "query" {
			_, _ = io.WriteString(w, `{"query":{"tokens":{"csrftoken":"T"}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"error":{"code":"editconflict","info":"Edit conflict"}}`)
	}))
	defer srv.Close()

	_, err := testClient(srv).Edit("A", "x", "s", 1)
	if err == nil {
		t.Fatal("want an error on editconflict")
	}
}

// editServer answers token requests and returns body for the edit itself.
func editServer(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") == "query" {
			_, _ = io.WriteString(w, `{"query":{"tokens":{"csrftoken":"T"}}}`)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
}

func TestEditNoChangeReturnsZero(t *testing.T) {
	srv := editServer(`{"edit":{"result":"Success","pageid":5,"title":"A","nochange":true}}`)
	defer srv.Close()

	rev, err := testClient(srv).Edit("A", "x", "s", 1)
	if err != nil || rev != 0 {
		t.Fatalf("Edit = (%d, %v), want (0, nil) for a null edit", rev, err)
	}
}

func TestEditSuccessWithoutRevisionIsAnError(t *testing.T) {
	srv := editServer(`{"edit":{"result":"Success","pageid":5,"title":"A"}}`)
	defer srv.Close()

	if _, err := testClient(srv).Edit("A", "x", "s", 1); err == nil {
		t.Fatal("want an error when success carries neither newrevid nor nochange")
	}
}

func TestCallReportsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"error":{"code":"readapidenied","info":"You need read permission."}}`)
	}))
	defer srv.Close()

	_, _, err := testClient(srv).CurrentRevID("A")
	if err == nil || !strings.Contains(err.Error(), "readapidenied") {
		t.Fatalf("err = %v, want the API's error code", err)
	}
}

// A wiki that is down answers with an HTML error page; every call reports the
// HTTP status rather than failing to parse the page as JSON.
func TestCallsReportHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "<html>Service Unavailable</html>")
	}))
	defer srv.Close()
	c := testClient(srv)

	calls := map[string]func() error{
		"Login":        func() error { return c.Login("u", "p") },
		"Edit":         func() error { _, err := c.Edit("A", "x", "s", 1); return err },
		"CurrentRevID": func() error { _, _, err := c.CurrentRevID("A"); return err },
		"FetchPages": func() error {
			_, err := c.FetchPages([]string{"A"})
			return err
		},
		"RevisionText": func() error { _, err := c.RevisionText(1); return err },
		"SiteInfo":     func() error { _, err := c.SiteInfo(); return err },
		"CategoryMembers": func() error {
			_, _, err := c.CategoryMembers("Water")
			return err
		},
	}
	for name, call := range calls {
		if err := call(); err == nil || !strings.Contains(err.Error(), "503") {
			t.Errorf("%s: err = %v, want the HTTP status", name, err)
		}
	}
}

func TestRevisionText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("revids"); got != "1234" {
			t.Errorf("revids = %q, want 1234", got)
		}
		_, _ = io.WriteString(w, `{"query":{"pages":[{"pageid":5,"title":"A",`+
			`"revisions":[{"revid":1234,"slots":{"main":{"content":"stored text"}}}]}]}}`)
	}))
	defer srv.Close()

	text, err := testClient(srv).RevisionText(1234)
	if err != nil || text != "stored text" {
		t.Fatalf("RevisionText = (%q, %v)", text, err)
	}
}

// An edit the wiki declines is told apart from a wiki or a connection that
// failed, through the real request path (#66).
func TestRefusedTellsTheWikiDecliningFromTheWikiFailing(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		refused    bool
	}{
		{"protected page", `{"error":{"code":"protectedpage","info":"This page has been protected"}}`, 200, true},
		{"edit conflict", `{"error":{"code":"editconflict","info":"Edit conflict"}}`, 200, true},
		{"abuse filter result", `{"edit":{"result":"Failure"}}`, 200, true},
		{"database error", `{"error":{"code":"internal_api_error_DBQueryError","info":"Database error"}}`, 200, false},
		{"read-only wiki", `{"error":{"code":"readonly","info":"The wiki is in read-only mode"}}`, 200, false},
		{"lost session", `{"error":{"code":"badtoken","info":"Invalid CSRF token"}}`, 200, false},
		{"server error", `oops`, 503, false},
		{"not json", `<html>`, 200, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				if r.Form.Get("meta") == "tokens" {
					_, _ = io.WriteString(w, `{"query":{"tokens":{"csrftoken":"t"}}}`)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			c := &Client{HTTP: srv.Client(), APIURL: srv.URL, UserAgent: "ua"}
			_, err := c.Edit("Page", "text", "s", 1)
			if err == nil {
				t.Fatal("Edit succeeded, want an error")
			}
			if got := Refused(err); got != tc.refused {
				t.Errorf("Refused(%v) = %v, want %v", err, got, tc.refused)
			}
		})
	}
}
