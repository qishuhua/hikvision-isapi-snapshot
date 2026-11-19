package oss

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qishuhua/hikvision-isapi-snapshot/internal/model"
)

func TestS3SignatureV4PutObject(t *testing.T) {
	payload := []byte{0xff, 0xd8, 0xff, 0xd9}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/bucket-one/path/image.jpg" {
			t.Errorf("unexpected request path %s", r.URL.Path)
		}
		hash := sha256.Sum256(payload)
		wantHash := hex.EncodeToString(hash[:])
		if r.Header.Get("x-amz-content-sha256") != wantHash {
			t.Error("payload hash mismatch")
		}
		stamp := r.Header.Get("x-amz-date")
		if len(stamp) != 16 {
			t.Errorf("invalid date %q", stamp)
		}
		scope := stamp[:8] + "/cn-shanghai/s3/aws4_request"
		canonical := "PUT\n/bucket-one/path/image.jpg\n\n" + "host:" + r.Host + "\n" + "x-amz-content-sha256:" + wantHash + "\n" + "x-amz-date:" + stamp + "\n\n" + "host;x-amz-content-sha256;x-amz-date\n" + wantHash
		stringToSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + shaHex([]byte(canonical))
		expectedSig := hex.EncodeToString(hmacSHA(sigKey("secret-key", stamp[:8], "cn-shanghai"), stringToSign))
		if !strings.Contains(r.Header.Get("Authorization"), "Signature="+expectedSig) {
			t.Errorf("unexpected Authorization signature")
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	client := New(5 * time.Second)
	client.http = srv.Client()
	err := client.Upload(context.Background(), model.Storage{Endpoint: srv.URL, Region: "cn-shanghai", Bucket: "bucket-one", AccessKeyID: "ak-test", SecretAccessKey: "secret-key", ForcePathStyle: true}, "path/image.jpg", payload)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOSSUsesVirtualHostedBucketName(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Host, "images.") {
			t.Errorf("expected virtual-hosted bucket, got host %s", r.Host)
		}
		if r.URL.Path != "/2026/test.jpg" {
			t.Errorf("expected object path, got %q", r.URL.Path)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // Only test TLS from httptest; never used by production.
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	client := New(5 * time.Second)
	client.http.Transport = tr
	cfg := model.Storage{Endpoint: srv.URL, Region: "cn-shanghai", Bucket: "images", AccessKeyID: "AK", SecretAccessKey: "SK"}
	if err := client.Upload(context.Background(), cfg, "2026/test.jpg", []byte("bytes")); err != nil {
		t.Fatal(err)
	}
}
