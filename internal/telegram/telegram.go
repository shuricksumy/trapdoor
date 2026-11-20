// Package telegram sends messages through a Telegram bot (Bot API
// sendMessage). Only the standard library; the web process calls it.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// APIBase is the Bot API address (replaced in tests).
var APIBase = "https://api.telegram.org"

var (
	tokenRe = regexp.MustCompile(`^[0-9]{5,15}:[A-Za-z0-9_-]{30,64}$`)
	chatRe  = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
)

// CheckToken reports whether s looks like a bot token from @BotFather
// ("123456789:AA…").
func CheckToken(s string) error {
	if !tokenRe.MatchString(s) {
		return errors.New("a bot token looks like 123456789:AAE… (from @BotFather)")
	}
	return nil
}

// ParseChats checks chat ids: numbers (users, groups: -100…) or @channel
// names, separated by commas or spaces. Duplicates are dropped.
func ParseChats(in []string) ([]string, error) {
	out := []string{}
	for _, s := range in {
		for _, c := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
			if !chatRe.MatchString(c) {
				return nil, fmt.Errorf("%q is not a chat id (a number like 123456789 or -1001234567890, or @channel)", c)
			}
			dup := false
			for _, o := range out {
				dup = dup || o == c
			}
			if !dup {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

var client = &http.Client{Timeout: 15 * time.Second}

// Send sends text to one chat. The token never appears in the error.
func Send(ctx context.Context, token, chat, text string) error {
	form := url.Values{"chat_id": {chat}, "text": {text}, "disable_web_page_preview": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, APIBase+"/bot"+token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("telegram: bad request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		// url.Error contains the URL, which contains the token.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("telegram: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode != http.StatusOK || !out.OK {
		if out.Description == "" {
			out.Description = resp.Status
		}
		return fmt.Errorf("telegram: %s", out.Description)
	}
	return nil
}
