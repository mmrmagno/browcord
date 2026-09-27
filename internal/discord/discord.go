package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	apiBase = "https://discord.com/api"
)

var snowflake = regexp.MustCompile(`^[0-9]{1,20}$`)

type Client struct {
	ID     string
	Secret string
	HTTP   *http.Client
	API    string
}

func New(id, secret string) *Client {
	return &Client{
		ID:     id,
		Secret: secret,
		HTTP:   &http.Client{Timeout: 10 * time.Second},
		API:    apiBase,
	}
}

func (c *Client) IsMember(ctx context.Context, accessToken, guildID string) (bool, error) {
	if !snowflake.MatchString(guildID) {
		return false, fmt.Errorf("discord: guild id %q is not a snowflake", guildID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.API+"/users/@me/guilds/"+guildID+"/member", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("discord: membership: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("discord: membership check returned %d", resp.StatusCode)
	}
}

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Global   string `json:"global_name"`
	Avatar   string `json:"avatar"`
}

func (u User) DisplayName() string {
	if u.Global != "" {
		return u.Global
	}
	return u.Username
}

type Identity struct {
	User    User
	GuildID string
}

func (c *Client) Exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{
		"client_id":     {c.ID},
		"client_secret": {c.Secret},
		"grant_type":    {"authorization_code"},
		"code":          {code},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.API+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("discord: token exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("discord: token exchange returned %d: %s", resp.StatusCode, body)
	}

	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return "", fmt.Errorf("discord: decode token: %w", err)
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("discord: token exchange returned no access token")
	}

	return payload.AccessToken, nil
}

func (c *Client) Identify(ctx context.Context, accessToken string) (Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.API+"/oauth2/@me", nil)
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("discord: identify: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("discord: identify returned %d", resp.StatusCode)
	}

	var payload struct {
		User User `json:"user"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return Identity{}, fmt.Errorf("discord: decode identity: %w", err)
	}
	if payload.User.ID == "" {
		return Identity{}, fmt.Errorf("discord: identity response carried no user id")
	}

	return Identity{User: payload.User}, nil
}
