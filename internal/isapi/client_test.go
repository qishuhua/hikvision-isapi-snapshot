package isapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qishuhua/hikvision-isapi-snapshot/internal/model"
)

func TestDigestSnapshotAndProxy(t *testing.T) {
	for _, mode := range []string{"streaming", "proxy"} {
		t.Run(mode, func(t *testing.T) {
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				expected := model.Camera{ChannelID: "201", EndpointMode: mode}
				expectedPath := strings.TrimPrefix(Endpoint(expected), "") // empty base URL, only path
				if r.URL.Path != expectedPath {
					t.Errorf("got path %q want %q", r.URL.Path, expectedPath)
				}
				hdr := r.Header.Get("Authorization")
				if hdr == "" {
					w.Header().Set("WWW-Authenticate", `Digest realm="nvr", nonce="abc123", qop="auth", algorithm=MD5`)
					w.WriteHeader(401)
					return
				}
				p := parseDigestChallenge(hdr)
				if p["username"] != "admin" || p["uri"] != r.URL.Path || p["nc"] != "00000001" {
					t.Errorf("invalid digest fields: %v", p)
				}
				a1 := hash("MD5", "admin:nvr:secret")
				a2 := hash("MD5", "GET:"+r.URL.Path)
				expectedResponse := hash("MD5", fmt.Sprintf("%s:abc123:00000001:%s:auth:%s", a1, p["cnonce"], a2))
				if p["response"] != expectedResponse {
					t.Errorf("digest mismatch")
				}
				w.Header().Set("Content-Type", "image/jpeg")
				w.Write([]byte{0xff, 0xd8, 0xff, 0xd9})
			}))
			defer srv.Close()
			cam := model.Camera{Name: "test", BaseURL: srv.URL, Username: "admin", Password: "secret", ChannelID: "201", EndpointMode: mode}
			got, err := New(5*time.Second).Capture(context.Background(), cam)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 4 || hits != 2 {
				t.Fatalf("unexpected JPEG or hits: %d %d", len(got), hits)
			}
		})
	}
}
func TestRejectXML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<xml>error</xml>")) }))
	defer srv.Close()
	_, err := New(time.Second).Capture(context.Background(), model.Camera{Name: "c", BaseURL: srv.URL, Username: "u", Password: "p", ChannelID: "101"})
	if err == nil || !strings.Contains(err.Error(), "non-JPEG") {
		t.Fatalf("unexpected error %v", err)
	}
}
func TestParseDigestQuotedComma(t *testing.T) {
	p := parseDigestChallenge(`Digest realm="alpha,beta", qop="auth,auth-int", nonce="xyz"`)
	if p["realm"] != "alpha,beta" || p["qop"] != "auth,auth-int" {
		t.Fatalf("%v", p)
	}
}
