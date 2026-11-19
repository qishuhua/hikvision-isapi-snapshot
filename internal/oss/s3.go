// Package oss implements the small S3 SigV4 PutObject subset needed for JPEG uploads.
// It intentionally uses Content-Length and a fully hashed payload (not aws-chunked),
// which is compatible with Alibaba Cloud OSS's S3 endpoint.
package oss

import (
	"bytes"
	"context"
	"crypto/hmac"
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

type Client struct{ http *http.Client }

func New(timeout time.Duration) *Client { return &Client{http: &http.Client{Timeout: timeout}} }
func hmacSHA(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}
func shaHex(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func sigKey(secret, date, region string) []byte {
	k := hmacSHA([]byte("AWS4"+secret), date)
	k = hmacSHA(k, region)
	k = hmacSHA(k, "s3")
	return hmacSHA(k, "aws4_request")
}
func escapePath(s string) string {
	// URL.EscapedPath escapes each segment, retaining '/' as an S3 object key separator.
	segs := strings.Split(s, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/" + strings.Join(segs, "/")
}
func (c *Client) Upload(ctx context.Context, config model.Storage, key string, data []byte) error {
	if err := model.ValidateStorage(&config); err != nil {
		return err
	}
	if config.SecretAccessKey == "" {
		return errors.New("OSS secret_access_key missing")
	}
	if len(data) == 0 {
		return errors.New("empty upload")
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil {
		return err
	}
	host := endpoint.Host
	path := "/" + key
	if config.ForcePathStyle {
		path = "/" + config.Bucket + path
	} else {
		host = config.Bucket + "." + host
	}
	escaped := escapePath(strings.TrimPrefix(path, "/"))
	target := endpoint.Scheme + "://" + host + escaped
	now := time.Now().UTC()
	stamp := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	payloadHash := shaHex(data)
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	headers := "host:" + host + "\n" + "x-amz-content-sha256:" + payloadHash + "\n" + "x-amz-date:" + stamp + "\n"
	if config.SessionToken != "" {
		signedHeaders += ";x-amz-security-token"
		headers += "x-amz-security-token:" + config.SessionToken + "\n"
	}
	requestText := "PUT\n" + escaped + "\n\n" + headers + "\n" + signedHeaders + "\n" + payloadHash
	scope := date + "/" + config.Region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + shaHex([]byte(requestText))
	sig := hex.EncodeToString(hmacSHA(sigKey(config.SecretAccessKey, date, config.Region), toSign))
	auth := "AWS4-HMAC-SHA256 Credential=" + config.AccessKeyID + "/" + scope + ", SignedHeaders=" + signedHeaders + ", Signature=" + sig
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "PUT", target, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", auth)
		req.Header.Set("x-amz-date", stamp)
		req.Header.Set("x-amz-content-sha256", payloadHash)
		if config.SessionToken != "" {
			req.Header.Set("x-amz-security-token", config.SessionToken)
		}
		req.Header.Set("Content-Type", "image/jpeg")
		resp, err := c.http.Do(req)
		if err == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			// Avoid echoing response body: remote error bodies can contain request metadata.
			last = fmt.Errorf("OSS PutObject HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
			if resp.StatusCode != 429 && resp.StatusCode < 500 {
				return last
			}
		} else {
			last = err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 300 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	return last
}
