// Package tvdb implements the fixed-destination TheTVDB v4 API.
package tvdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/httpclient"
)

const endpoint = "https://api4.thetvdb.com/v4"

// Provider bodies and transport errors can contain credentials and are never
// returned. The console translates credential errors and groups other errors.
var (
	ErrKeyRequired     = errors.New("Enter a TheTVDB API key.")
	ErrUnauthorized    = errors.New("TheTVDB rejected the API key.")
	ErrUnavailable     = errors.New("Could not connect to TheTVDB.")
	ErrInvalidResponse = errors.New("TheTVDB returned an invalid response.")
	ErrRateLimited     = errors.New("TheTVDB rate limit was reached. Try again later.")
)

var client = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func request(ctx context.Context, method, path, token string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, bytes.NewReader(body))
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", httpclient.UserAgent())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusTooManyRequests:
		return ErrRateLimited
	default:
		return ErrUnavailable
	}
	const maxResponse = 8 << 20
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil || len(raw) > maxResponse || json.Unmarshal(raw, out) != nil {
		return ErrInvalidResponse
	}
	return nil
}

func login(ctx context.Context, key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", ErrKeyRequired
	}
	body, _ := json.Marshal(map[string]string{"apikey": key})
	var res struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
		Status string `json:"status"`
	}
	if err := request(ctx, http.MethodPost, "/login", "", body, &res); err != nil {
		return "", err
	}
	if res.Status != "success" || strings.TrimSpace(res.Data.Token) == "" {
		return "", ErrInvalidResponse
	}
	return res.Data.Token, nil
}

// Test verifies a key without writing it or changing catalogue membership.
func Test(ctx context.Context, key string) error {
	_, err := login(ctx, key)
	return err
}

// SeriesIDs reads a bounded prefix of the upstream series list, signing in
// once for the entire refresh. Membership is cached by the catalogue store.
func SeriesIDs(ctx context.Context, key string, limit int) ([]int64, error) {
	token, err := login(ctx, key)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, limit)
	seen := make(map[int64]bool, limit)
	for page := 0; page < 5 && len(ids) < limit; page++ {
		var res struct {
			Data []struct {
				ID int64 `json:"id"`
			} `json:"data"`
			Status string `json:"status"`
			Links  struct {
				Next string `json:"next"`
			} `json:"links"`
		}
		if err := request(ctx, http.MethodGet, "/series?page="+strconv.Itoa(page), token, nil, &res); err != nil {
			return nil, err
		}
		if res.Status != "success" || res.Data == nil {
			return nil, ErrInvalidResponse
		}
		for _, record := range res.Data {
			if record.ID <= 0 || seen[record.ID] {
				continue
			}
			seen[record.ID] = true
			ids = append(ids, record.ID)
			if len(ids) == limit {
				return ids, nil
			}
		}
		if res.Links.Next == "" || len(res.Data) == 0 {
			return ids, nil
		}
	}
	return ids, nil
}
