package local

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"flexconnect/internal/buildinfo"
	"flexconnect/internal/ipc"
	"flexconnect/internal/logging"
	"flexconnect/internal/types"
)

var (
	debugEnabled bool
	debugLogger  = logging.WithComponent("flexconnect-local")
)

func SetDebug(enabled bool) {
	debugEnabled = enabled
}

func localDebug(v ...interface{}) {
	if !debugEnabled {
		return
	}
	debugLogger.Debug(strings.TrimSpace(fmt.Sprintln(v...)))
}

type Client struct {
	Socket    string
	Transport http.RoundTripper
	Dial      func(context.Context, string) (net.Conn, error)

	once sync.Once
	hc   *http.Client
}

type IncompatibleAPIError struct {
	ClientVersion string
	DaemonVersion string
}

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("local API returned HTTP %d", e.StatusCode)
	}
	return e.Message
}

func (e *IncompatibleAPIError) Error() string {
	return fmt.Sprintf("local API version mismatch: client=%s daemon=%s", e.ClientVersion, e.DaemonVersion)
}

func (c *Client) socket() string {
	if c.Socket != "" {
		return c.Socket
	}
	return ipc.DefaultSocketPath()
}

func (c *Client) dialer() func(context.Context, string) (net.Conn, error) {
	if c.Dial != nil {
		return c.Dial
	}
	return func(ctx context.Context, _ string) (net.Conn, error) {
		return ipc.DialContext(ctx, c.socket())
	}
}

func (c *Client) httpClient() *http.Client {
	c.once.Do(func() {
		transport := c.Transport
		if transport == nil {
			transport = &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return c.dialer()(ctx, c.socket())
				},
			}
		}
		c.hc = &http.Client{Transport: transport}
	})
	return c.hc
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	localDebug("HTTP request", method, path)
	req, err := http.NewRequestWithContext(ctx, method, "http://"+ipc.LocalAPIHost+path, body)
	if err != nil {
		localDebug("HTTP request create failed", err)
		return nil, err
	}
	start := time.Now()
	res, err := c.httpClient().Do(req)
	elapsed := time.Since(start)
	if err != nil {
		localDebug("HTTP request failed", method, path, "elapsed", elapsed, "err", err)
		return nil, err
	}
	localDebug("HTTP response", method, path, "status", res.StatusCode, "elapsed", elapsed)
	return res, nil
}

func (c *Client) Status(ctx context.Context) (*types.Status, error) {
	var out types.Status
	return &out, c.getJSON(ctx, "/v1/status", &out)
}

func (c *Client) Health(ctx context.Context) (*types.Health, error) {
	var out types.Health
	if err := c.getJSON(ctx, "/v1/health", &out); err != nil {
		return nil, err
	}
	if out.Status != "ok" {
		return nil, fmt.Errorf("flexconnectd is not ready: status=%q", out.Status)
	}
	if out.APIVersion != buildinfo.LocalAPIVersion {
		return nil, &IncompatibleAPIError{ClientVersion: buildinfo.LocalAPIVersion, DaemonVersion: out.APIVersion}
	}
	return &out, nil
}

func (c *Client) Profiles(ctx context.Context) ([]types.Profile, error) {
	var out []types.Profile
	return out, c.getJSON(ctx, "/v1/profiles", &out)
}

func (c *Client) CurrentProfile(ctx context.Context) (*types.Profile, error) {
	var out types.Profile
	return &out, c.getJSON(ctx, "/v1/profiles/current", &out)
}

func (c *Client) CreateProfile(ctx context.Context, profile types.Profile, password string) (*types.Profile, error) {
	payload := types.ProfileUpsertRequest{Profile: profile, Password: password}
	var out types.Profile
	return &out, c.sendJSON(ctx, http.MethodPut, "/v1/profiles", payload, &out, http.StatusCreated)
}

func (c *Client) UpdateProfile(ctx context.Context, id string, req types.ProfileUpdateRequest) (*types.Profile, error) {
	var out types.Profile
	return &out, c.sendJSON(ctx, http.MethodPut, "/v1/profiles/"+id, req, &out, http.StatusOK)
}

func (c *Client) SwitchProfile(ctx context.Context, id string) error {
	return c.expectNoContent(ctx, http.MethodPost, "/v1/profiles/"+id+"/switch", nil)
}

func (c *Client) DeleteProfile(ctx context.Context, id string) error {
	return c.expectNoContent(ctx, http.MethodDelete, "/v1/profiles/"+id, nil)
}

func (c *Client) Login(ctx context.Context, req types.LoginRequest) error {
	return c.expectNoContentJSON(ctx, http.MethodPost, "/v1/login", req)
}

func (c *Client) Connect(ctx context.Context, id string) error {
	if id == "" {
		return c.ConnectCurrent(ctx)
	}
	return c.expectNoContent(ctx, http.MethodPost, "/v1/connect/"+id, nil)
}

func (c *Client) ConnectCurrent(ctx context.Context) error {
	return c.expectNoContent(ctx, http.MethodPost, "/v1/connect", nil)
}

func (c *Client) Disconnect(ctx context.Context) error {
	return c.expectNoContent(ctx, http.MethodPost, "/v1/disconnect", nil)
}

func (c *Client) UpdateRoutes(ctx context.Context, id string, req types.RouteUpdateRequest) (*types.Profile, error) {
	var out types.Profile
	return &out, c.sendJSON(ctx, http.MethodPut, "/v1/routes/"+id, req, &out, http.StatusOK)
}

func (c *Client) Logs(ctx context.Context) ([]types.LogEntry, error) {
	var out []types.LogEntry
	return out, c.getJSON(ctx, "/v1/logs", &out)
}

func (c *Client) Diagnostics(ctx context.Context) (*types.Diagnostics, error) {
	var out types.Diagnostics
	return &out, c.getJSON(ctx, "/v1/diagnostics", &out)
}

func (c *Client) Traffic(ctx context.Context) (*types.TrafficSnapshot, error) {
	var out types.TrafficSnapshot
	return &out, c.getJSON(ctx, "/v1/traffic", &out)
}

func (c *Client) DiagnosticsText(ctx context.Context) (string, error) {
	diag, err := c.Diagnostics(ctx)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(diag, "", "  ")
	if err != nil {
		return "", fmt.Errorf("format diagnostics: %w", err)
	}
	return string(data), nil
}

func (c *Client) Watch(ctx context.Context) (*Watcher, error) {
	localDebug("GET", "/v1/watch")
	res, err := c.do(ctx, http.MethodGet, "/v1/watch", nil)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		defer res.Body.Close()
		err := responseError(res)
		localDebug("/v1/watch unexpected status", res.StatusCode, err)
		return nil, err
	}
	localDebug("/v1/watch started")
	return &Watcher{ctx: ctx, res: res, dec: json.NewDecoder(res.Body)}, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	localDebug("GET", path)
	res, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		err := responseError(res)
		localDebug("GET", path, "unexpected status", res.StatusCode, "err", err)
		return err
	}
	localDebug("GET", path, "ok")
	return json.NewDecoder(res.Body).Decode(out)
}

func (c *Client) sendJSON(ctx context.Context, method, path string, in, out any, want int) error {
	localDebug(method, path)
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		localDebug(method, path, "payload_bytes", len(b))
		body = strings.NewReader(string(b))
	}
	res, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != want {
		err := responseError(res)
		localDebug(method, path, "unexpected status", res.StatusCode, "err", err)
		return err
	}
	if out == nil {
		return nil
	}
	localDebug(method, path, "ok")
	return json.NewDecoder(res.Body).Decode(out)
}

func (c *Client) expectNoContent(ctx context.Context, method, path string, body io.Reader) error {
	localDebug(method, path)
	res, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		err := responseError(res)
		localDebug(method, path, "unexpected status", res.StatusCode, "err", err)
		return err
	}
	localDebug(method, path, "ok")
	return nil
}

func responseError(res *http.Response) error {
	const maxErrorBytes = 64 * 1024
	all, err := io.ReadAll(io.LimitReader(res.Body, maxErrorBytes+1))
	if err != nil {
		return fmt.Errorf("read local API error response: %w", err)
	}
	message := strings.TrimSpace(string(all))
	if len(all) > maxErrorBytes {
		message = strings.TrimSpace(string(all[:maxErrorBytes])) + "..."
	}
	if message == "" {
		message = http.StatusText(res.StatusCode)
	}
	return &APIError{StatusCode: res.StatusCode, Message: message}
}

func (c *Client) expectNoContentJSON(ctx context.Context, method, path string, in any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.expectNoContent(ctx, method, path, strings.NewReader(string(b)))
}

type Watcher struct {
	ctx context.Context
	res *http.Response
	dec *json.Decoder
}

func (w *Watcher) Next() (types.Notify, error) {
	localDebug("watch next")
	var out types.Notify
	if err := w.dec.Decode(&out); err != nil {
		if w.ctx.Err() != nil {
			return types.Notify{}, w.ctx.Err()
		}
		localDebug("watch decode error", err)
		return types.Notify{}, err
	}
	localDebug("watch event", out.Event)
	return out, nil
}

func (w *Watcher) Close() error {
	return w.res.Body.Close()
}
