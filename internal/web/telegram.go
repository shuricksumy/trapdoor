package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/telegram"
)

const (
	settingTelegram       = "telegram"
	settingTelegramStatus = "telegram_status"
)

// TelegramSettings is GET / PUT /api/v1/telegram: a bot that sends the
// admins' notifications to one or more chats.
type TelegramSettings struct {
	Enabled bool `json:"enabled"`
	// Token of the bot (from @BotFather); never sent back.
	Token string `json:"token,omitempty"`
	// Chats get the notifications: user or group ids, or @channel.
	Chats  []string `json:"chats"`
	Notify Notify   `json:"notify"`
	// TokenSet (answer only): a token is stored.
	TokenSet bool `json:"token_set"`
	// ClearToken (request only) removes the stored token; an empty token
	// otherwise keeps it.
	ClearToken bool `json:"clear_token,omitempty"`
}

func (s *Server) telegramSettings(ctx context.Context) (TelegramSettings, error) {
	ts := TelegramSettings{Notify: Notify{BackupFailed: true, NodeDown: true, CertExpiry: true, UsageLimit: true}}
	_, err := s.Stats.DB.Setting(ctx, settingTelegram, &ts)
	return ts, err
}

func (ts TelegramSettings) on() bool { return ts.Enabled && ts.Token != "" }

// sendTelegram sends text to the chats and records the outcome. A chat that
// fails does not stop the others; the first error is returned.
func (s *Server) sendTelegram(ctx context.Context, token string, chats []string, text string) error {
	send := s.SendTelegram
	if send == nil {
		send = telegram.Send
	}
	var first error
	for _, c := range chats {
		if err := send(ctx, token, c, text); err != nil {
			s.Log.Warn("telegram message not sent", "chat", c, "err", err)
			if first == nil {
				first = err
			}
		}
	}
	st := EmailStatus{Time: s.Now().UTC(), OK: first == nil, Subject: firstLine(text)}
	if first != nil {
		st.Error = first.Error()
	}
	_ = s.Stats.DB.SetSetting(context.WithoutCancel(ctx), settingTelegramStatus, st)
	return first
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func (s *Server) registerTelegramRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/telegram", s.handleGetTelegram)
	mux.HandleFunc("PUT /api/v1/telegram", s.handleSetTelegram)
	mux.HandleFunc("POST /api/v1/telegram/test", s.handleTestTelegram)
}

func (s *Server) handleGetTelegram(w http.ResponseWriter, r *http.Request) {
	ts, err := s.telegramSettings(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ts.TokenSet, ts.Token = ts.Token != "", ""
	out := struct {
		TelegramSettings
		Last *EmailStatus `json:"last,omitempty"`
	}{TelegramSettings: ts}
	var st EmailStatus
	if ok, _ := s.Stats.DB.Setting(r.Context(), settingTelegramStatus, &st); ok {
		out.Last = &st
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleSetTelegram(w http.ResponseWriter, r *http.Request) {
	var req TelegramSettings
	if !decodeJSON(w, r, &req) {
		return
	}
	old, err := s.telegramSettings(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	switch {
	case req.ClearToken:
		req.Token = ""
	case req.Token == "":
		req.Token = old.Token
	default:
		if err := telegram.CheckToken(req.Token); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Chats, err = telegram.ParseChats(req.Chats); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Enabled && req.Token == "" {
		httpx.WriteError(w, http.StatusBadRequest, "add the bot token first")
		return
	}
	req.TokenSet, req.ClearToken = false, false
	if err := s.Stats.DB.SetSetting(r.Context(), settingTelegram, req); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "telegram.settings", strings.Join(req.Chats, ","))
	req.TokenSet, req.Token = req.Token != "", ""
	httpx.WriteJSON(w, http.StatusOK, req)
}

func (s *Server) handleTestTelegram(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Chat string `json:"chat"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	ts, err := s.telegramSettings(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ts.on() {
		httpx.WriteError(w, http.StatusBadRequest, "set up and turn on Telegram first")
		return
	}
	chats := ts.Chats
	if req.Chat != "" {
		if chats, err = telegram.ParseChats([]string{req.Chat}); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if len(chats) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, "no chat to send it to")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.sendTelegram(ctx, ts.Token, chats, "trapdoor: test message. Telegram works."+s.baseURLLine()); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "not sent: "+err.Error())
		return
	}
	s.audit(r, "telegram.test", strings.Join(chats, ","))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent_to": chats})
}
