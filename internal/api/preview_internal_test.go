package api

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHostOnlyCookie(t *testing.T) {
	for _, tc := range []struct {
		in, out string
		keep    bool
	}{
		{"a=1; Path=/; Domain=apps.test; HttpOnly", "a=1; Path=/; HttpOnly", true},
		{"a=1; Domain=.apps.test", "a=1", true},
		{"a=1; Path=/", "a=1; Path=/", true},
		{"a=1; Domain=a; Domain=", "", false}, // repeated: browsers may keep the first
		{"a=1; Domain=ａｐｐｓ.test", "", false},  // fullwidth: browsers may map it to apps.test
		{"a=1; Domain=apps.test; Domain=other", "", false},
		{"ah_preview=x; Path=/", "", false}, // shadows the gate
		{"__Host-ah_preview=x; Path=/; Secure", "", false},
		{"AH_PREVIEW_STATE=x", "", false},
		{"agenthold_session=x", "", false},
		{"=x", "", false},
		{"a=1; Secure; SameSite=None; Partitioned", "a=1; Secure; SameSite=None; Partitioned", true},
	} {
		out, keep := hostOnlyCookie(tc.in)
		if keep != tc.keep || out != tc.out {
			t.Errorf("hostOnlyCookie(%q) = %q, %v; want %q, %v", tc.in, out, keep, tc.out, tc.keep)
		}
	}
}

func TestAgentholdTokens(t *testing.T) {
	for _, tc := range []struct {
		header string
		bearer bool
		strip  bool
	}{
		{"Bearer ahp_abc", true, true},
		{"bearer ahp_abc", true, true},
		{"BEARER   ahp_abc ", true, true},
		{"Token ahp_abc", false, true},
		{"Basic " + base64.StdEncoding.EncodeToString([]byte("u:ahp_abc")), false, true},
		{"Bearer app-token", false, false},
		{"Basic " + base64.StdEncoding.EncodeToString([]byte("u:p")), false, false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", tc.header)
		if _, ok := agentholdBearer(r); ok != tc.bearer {
			t.Errorf("agentholdBearer(%q) = %v", tc.header, ok)
		}
		if got := carriesAgentholdToken([]string{tc.header}); got != tc.strip {
			t.Errorf("carriesAgentholdToken(%q) = %v", tc.header, got)
		}
	}
}

func TestPreviewSlots(t *testing.T) {
	a := &API{Deps: Deps{Now: time.Now}, wakes: wakes{slots: map[string]chan struct{}{}}}
	var releases []func()
	for i := 0; i < previewConcurrency; i++ {
		rel, ok := a.previewSlot("exp_1")
		if !ok {
			t.Fatalf("slot %d refused", i)
		}
		releases = append(releases, rel)
	}
	if _, ok := a.previewSlot("exp_1"); ok {
		t.Fatal("slot beyond the cap granted")
	}
	if _, ok := a.previewSlot("exp_2"); !ok {
		t.Fatal("another exposure is limited too")
	}
	releases[0]()
	if _, ok := a.previewSlot("exp_1"); !ok {
		t.Fatal("released slot not reusable")
	}
}
