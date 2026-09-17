package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type client struct {
	base, token string
	http        *http.Client
}

func newClient(home, base string) (*client, error) {
	path := filepath.Join(home, "run", "daemon.token")
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return nil, fmt.Errorf("Can't read the daemon token (%s). Run swarm install.", path)
	}
	return &client{base: strings.TrimRight(base, "/"), token: strings.TrimSpace(string(b)),
		http: &http.Client{Timeout: 5 * time.Minute}}, nil
}

// do sends a request; a non-2xx answer becomes an error with the API's message.
func (c *client) do(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if method != http.MethodGet {
		req.Header.Set("X-Swarm-Via", "cli") // contracts W7
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("The daemon isn't running at %s. Run swarm install.", c.base)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			return errors.New(e.Error.Message)
		}
		return fmt.Errorf("daemon returned %d", resp.StatusCode)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}
