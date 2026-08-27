// Package kube is a minimal read-only client for the local Kubernetes API server,
// built on the standard library and the service account mount.
package kube

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	DefaultTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	DefaultCAPath    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"

	RequestTimeout = 10 * time.Second
	maxBodyBytes   = 16 << 20
)

// Client talks to one API server. BaseURL, TokenPath and HTTPClient are exported so
// tests can point at an httptest server and so a local `kubectl proxy` can be used.
type Client struct {
	BaseURL    string
	TokenPath  string
	HTTPClient *http.Client
}

// New builds an in-cluster client from the standard service account mount.
func New() (*Client, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	if host == "" {
		return nil, errors.New("KUBERNETES_SERVICE_HOST is not set; not running in a cluster")
	}
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if port == "" {
		port = "443"
	}

	pool, err := caPool(DefaultCAPath)
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}

	return &Client{
		BaseURL:    "https://" + net.JoinHostPort(host, port),
		TokenPath:  DefaultTokenPath,
		HTTPClient: &http.Client{Transport: transport, Timeout: RequestTimeout},
	}, nil
}

func caPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("cluster CA at %s contains no usable certificates", path)
	}
	return pool, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: RequestTimeout}
}

// GetJSON fetches an absolute API path and decodes it into out. what names the
// payload in the decode error, e.g. "application list".
func (c *Client) GetJSON(ctx context.Context, path, what string, out any) error {
	// The projected token rotates, so it is re-read on every request.
	token, err := os.ReadFile(c.TokenPath)
	if err != nil {
		return fmt.Errorf("read service account token: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	// An empty token means an unauthenticated local proxy (kubectl proxy); sending
	// an empty bearer header makes the API server reject the request with 401.
	if t := strings.TrimSpace(string(token)); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("query kubernetes api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body := io.LimitReader(resp.Body, maxBodyBytes)

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(body, 512))
		return &StatusError{Code: resp.StatusCode, Body: Sanitize(string(snippet))}
	}

	if err := json.NewDecoder(body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", what, err)
	}
	return nil
}

// StatusError is a non-200 answer from the API server. The code is kept separate so
// callers can tell an RBAC denial from an outage.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("kubernetes api returned %d: %s", e.Code, e.Body)
}

// Sanitize flattens whitespace so an API error body stays on one log line.
func Sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		out = append(out, r)
	}
	return string(out)
}

// FromCache appends resourceVersion=0, which asks the API server to answer from its
// watch cache instead of doing a quorum read from etcd. These lists run to megabytes
// on a busy cluster, and skipping the quorum read roughly halved the time they took.
//
// The trade is that the answer can lag the very latest write by a moment. That costs
// nothing here: the result is cached for the refresh interval anyway, and the page
// prints how old its data is.
func FromCache(path string) string {
	if strings.Contains(path, "?") {
		return path + "&resourceVersion=0"
	}
	return path + "?resourceVersion=0"
}
