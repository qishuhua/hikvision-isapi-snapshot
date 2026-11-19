// Package isapi retrieves a single image from Hikvision IPC/NVR using HTTP Digest auth.
package isapi

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/qishuhua/hikvision-isapi-snapshot/internal/model"
)

const maxJPEG = 16 << 20

type Client struct{ http *http.Client }

func New(timeout time.Duration) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // Cameras are usually on an internal network; no environment proxy.
	tr.MaxIdleConnsPerHost = 10
	return &Client{http: &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) Capture(ctx context.Context, camera model.Camera) ([]byte, error) {
	if err := model.ValidateCamera(&camera); err != nil {
		return nil, err
	}
	if camera.Password == "" {
		return nil, errors.New("camera password missing")
	}
	path := "/ISAPI/Streaming/channels/" + camera.ChannelID + "/picture"
	if camera.EndpointMode == "proxy" {
		path = "/ISAPI/ContentMgmt/StreamingProxy/channels/" + camera.ChannelID + "/picture"
	}
	target := camera.BaseURL + path
	request := func(auth string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "image/jpeg")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		return c.http.Do(req)
	}
	resp, err := request("")
	if err != nil {
		return nil, fmt.Errorf("camera request: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenges := resp.Header.Values("WWW-Authenticate")
		resp.Body.Close()
		var hdr string
		for _, ch := range challenges {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ch)), "digest ") {
				hdr, err = digestHeader(ch, http.MethodGet, path, camera.Username, camera.Password)
				if err != nil {
					return nil, fmt.Errorf("digest: %w", err)
				}
				break
			}
		}
		if hdr == "" {
			return nil, errors.New("camera did not offer supported Digest authentication")
		}
		resp, err = request(hdr)
		if err != nil {
			return nil, fmt.Errorf("authenticated camera request: %w", err)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("camera HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxJPEG+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxJPEG {
		return nil, errors.New("snapshot exceeds 16 MiB limit")
	}
	if len(b) < 4 || b[0] != 0xff || b[1] != 0xd8 || b[len(b)-2] != 0xff || b[len(b)-1] != 0xd9 {
		return nil, errors.New("camera returned non-JPEG data (check channel ID and endpoint mode)")
	}
	return b, nil
}

// parseDigestChallenge handles commas inside quoted parameter values.
func parseDigestChallenge(s string) map[string]string {
	s = strings.TrimSpace(s)
	if len(s) >= 7 && strings.EqualFold(s[:7], "Digest ") {
		s = s[7:]
	}
	out := map[string]string{}
	for len(s) > 0 {
		s = strings.TrimLeft(s, " ,\t")
		if s == "" {
			break
		}
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(s[:eq]))
		s = strings.TrimLeft(s[eq+1:], " \t")
		var val string
		if strings.HasPrefix(s, "\"") {
			s = s[1:]
			var b strings.Builder
			escaped := false
			i := 0
			for ; i < len(s); i++ {
				c := s[i]
				if escaped {
					b.WriteByte(c)
					escaped = false
					continue
				}
				if c == '\\' {
					escaped = true
					continue
				}
				if c == '"' {
					i++
					break
				}
				b.WriteByte(c)
			}
			val = b.String()
			s = s[i:]
		} else {
			i := strings.IndexByte(s, ',')
			if i < 0 {
				val = strings.TrimSpace(s)
				s = ""
			} else {
				val = strings.TrimSpace(s[:i])
				s = s[i:]
			}
		}
		out[key] = val
	}
	return out
}
func hash(algo, input string) string {
	if strings.HasPrefix(strings.ToUpper(algo), "SHA-256") {
		sum := sha256.Sum256([]byte(input))
		return hex.EncodeToString(sum[:])
	}
	sum := md5.Sum([]byte(input))
	return hex.EncodeToString(sum[:])
}
func digestHeader(challenge, method, uri, username, password string) (string, error) {
	p := parseDigestChallenge(challenge)
	realm, nonce := p["realm"], p["nonce"]
	if realm == "" || nonce == "" {
		return "", errors.New("realm or nonce missing")
	}
	algo := p["algorithm"]
	if algo == "" {
		algo = "MD5"
	}
	switch strings.ToUpper(algo) {
	case "MD5", "MD5-SESS", "SHA-256", "SHA-256-SESS":
	default:
		return "", fmt.Errorf("unsupported digest algorithm %q", algo)
	}
	qop := ""
	for _, q := range strings.Split(p["qop"], ",") {
		if strings.TrimSpace(q) == "auth" {
			qop = "auth"
			break
		}
	}
	if p["qop"] != "" && qop == "" {
		return "", errors.New("unsupported Digest qop; expected auth")
	}
	cnonceBytes := make([]byte, 12)
	if _, err := rand.Read(cnonceBytes); err != nil {
		return "", err
	}
	cnonce := hex.EncodeToString(cnonceBytes)
	a1 := hash(algo, username+":"+realm+":"+password)
	if strings.HasSuffix(strings.ToUpper(algo), "-SESS") {
		a1 = hash(algo, a1+":"+nonce+":"+cnonce)
	}
	a2 := hash(algo, method+":"+uri)
	response := hash(algo, a1+":"+nonce+":"+a2)
	if qop != "" {
		response = hash(algo, a1+":"+nonce+":00000001:"+cnonce+":"+qop+":"+a2)
	}
	escaped := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "\"", "\\\"") }
	h := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm=%s`, escaped(username), escaped(realm), escaped(nonce), escaped(uri), response, algo)
	if qop != "" {
		h += fmt.Sprintf(`, qop=auth, nc=00000001, cnonce="%s"`, cnonce)
	}
	if opaque := p["opaque"]; opaque != "" {
		h += fmt.Sprintf(`, opaque="%s"`, escaped(opaque))
	}
	return h, nil
}

// Endpoint returns the picture URL useful for diagnostics, with no credentials.
func Endpoint(camera model.Camera) string {
	base := strings.TrimRight(camera.BaseURL, "/")
	if camera.EndpointMode == "proxy" {
		return base + "/ISAPI/ContentMgmt/StreamingProxy/channels/" + url.PathEscape(camera.ChannelID) + "/picture"
	}
	return base + "/ISAPI/Streaming/channels/" + url.PathEscape(camera.ChannelID) + "/picture"
}
