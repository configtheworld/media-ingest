// Package jellyfin notifies a Jellyfin server about published files.
//
// Endpoints (checked against the Jellyfin OpenAPI spec, api.jellyfin.org,
// "stable", API version 12.2.0):
//
//	POST /Library/Media/Updated  body {"Updates":[{"Path":..,"UpdateType":"Created|Modified|Deleted"}]} -> 204
//	POST /Library/Refresh        (full scan, needs an elevated key)                                     -> 204
//
// Authentication uses the "Authorization: MediaBrowser Token=..." header;
// the legacy X-Emby-Token header and api_key query parameter are deprecated.
package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// Update types understood by Jellyfin.
const (
	Created  = "Created"
	Modified = "Modified"
	Deleted  = "Deleted"
)

// Update is one changed file, relative to MEDIA_ROOT.
type Update struct {
	Path string // relative to MEDIA_ROOT
	Type string
}

// Client talks to one Jellyfin server.
type Client struct {
	base, key, prefix string
	http              *http.Client
}

// New returns nil if url is empty (hook disabled). prefix is MEDIA_ROOT as
// the Jellyfin server sees it, e.g. "/data/media".
func New(baseURL, apiKey, prefix string) *Client {
	if baseURL == "" {
		return nil
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), key: apiKey, prefix: prefix, http: &http.Client{Timeout: 15 * time.Second}}
}

// Result describes what the hook did; it is stored and shown in history.
type Result struct {
	Method string `json:"method"` // media_updated | refresh | failed
	Error  string `json:"error,omitempty"`
}

func (r Result) String() string {
	if r.Error != "" {
		return r.Method + ": " + r.Error
	}
	return r.Method
}

// Notify reports the updates. It never returns an error: failures are
// reported in the result ("fails soft").
func (c *Client) Notify(ctx context.Context, updates []Update) Result {
	if len(updates) == 0 {
		return Result{Method: "none"}
	}
	type dto struct {
		Path       string `json:"Path"`
		UpdateType string `json:"UpdateType"`
	}
	body := struct {
		Updates []dto `json:"Updates"`
	}{}
	for _, u := range updates {
		body.Updates = append(body.Updates, dto{Path: path.Join(c.prefix, u.Path), UpdateType: u.Type})
	}
	b, _ := json.Marshal(body)
	err := c.post(ctx, "/Library/Media/Updated", b)
	if err == nil {
		return Result{Method: "media_updated"}
	}
	first := err
	if err := c.post(ctx, "/Library/Refresh", nil); err != nil {
		return Result{Method: "failed", Error: fmt.Sprintf("media updated: %v; refresh: %v", first, err)}
	}
	return Result{Method: "refresh", Error: "media updated: " + first.Error()}
}

func (c *Client) post(ctx context.Context, endpoint string, body []byte) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+endpoint, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", `MediaBrowser Token="`+c.key+`"`)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL from the message
		}
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
