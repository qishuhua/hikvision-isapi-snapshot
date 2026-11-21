// Package isapi retrieves a single image from Hikvision IPC/NVR using HTTP Digest auth.
package isapi

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
	if camera.Width > 0 {
		path += fmt.Sprintf("?videoResolutionWidth=%d&videoResolutionHeight=%d", camera.Width, camera.Height)
	}
	resp, err := c.get(ctx, camera, path)
	if err != nil {
		return nil, err
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

func (c *Client) get(ctx context.Context, camera model.Camera, path string) (*http.Response, error) {
	request := func(auth string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, camera.BaseURL+path, nil)
		if err != nil {
			return nil, err
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		return c.http.Do(req)
	}
	resp, err := request("")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	challenges := resp.Header.Values("WWW-Authenticate")
	resp.Body.Close()
	for _, ch := range challenges {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ch)), "digest ") {
			hdr, err := digestHeader(ch, http.MethodGet, path, camera.Username, camera.Password)
			if err != nil {
				return nil, err
			}
			return request(hdr)
		}
	}
	return nil, errors.New("camera did not offer supported Digest authentication")
}

type Channel struct {
	ID   string
	Name string
}

func (c *Client) OnlineChannels(ctx context.Context, camera model.Camera) ([]Channel, error) {
	read := func(path string, dst any) error {
		resp, err := c.get(ctx, camera, path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("channel query HTTP %d", resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
		if err != nil {
			return err
		}
		if len(b) > 4<<20 {
			return errors.New("channel response too large")
		}
		return xml.Unmarshal(b, dst)
	}
	var names struct {
		XMLName xml.Name
		Items   []struct {
			ID   int    `xml:"id"`
			Name string `xml:"name"`
		} `xml:"InputProxyChannel"`
	}
	if err := read("/ISAPI/ContentMgmt/InputProxy/channels", &names); err != nil {
		return nil, err
	}
	if names.XMLName.Local != "InputProxyChannelList" {
		return nil, errors.New("unexpected channel list")
	}
	var statuses struct {
		XMLName xml.Name
		Items   []struct {
			ID      int    `xml:"id"`
			Online  string `xml:"online"`
			Streams []int  `xml:"streamingProxyChannelIdList>streamingProxyChannelId"`
		} `xml:"InputProxyChannelStatus"`
	}
	if err := read("/ISAPI/ContentMgmt/InputProxy/channels/status", &statuses); err != nil {
		return nil, err
	}
	if statuses.XMLName.Local != "InputProxyChannelStatusList" {
		return nil, errors.New("unexpected channel status list")
	}
	nameMap := map[int]string{}
	for _, n := range names.Items {
		nameMap[n.ID] = strings.TrimSpace(n.Name)
	}
	allowed := map[string]bool{}
	for _, id := range strings.Split(camera.Channels, ",") {
		allowed[strings.TrimSpace(id)] = true
	}
	var out []Channel
	seen := map[string]bool{}
	for _, st := range statuses.Items {
		if st.ID <= 0 || !(strings.EqualFold(strings.TrimSpace(st.Online), "true") || strings.TrimSpace(st.Online) == "1") {
			continue
		}
		ids := st.Streams
		if len(ids) == 0 {
			ids = []int{st.ID*100 + 1}
		}
		if camera.Channels == "all" {
			ids = ids[:1]
		} else {
			ids = nil
			for id := range allowed {
				n, _ := strconv.Atoi(id)
				matches := n/100 == st.ID
				for _, stream := range st.Streams {
					if stream == n {
						matches = true
					}
				}
				if matches {
					ids = append(ids, n)
				}
			}
		}
		for _, id := range ids {
			key := strconv.Itoa(id)
			if id > 0 && !seen[key] {
				seen[key] = true
				out = append(out, Channel{ID: key, Name: nameMap[st.ID]})
			}
		}
	}
	return out, nil
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
