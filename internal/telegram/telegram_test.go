package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const token = "123456789:AAEhBP0av28BmFbW3Eq7Yqk0WbqXyZabc12"

func TestSend(t *testing.T) {
	var got struct{ path, chat, text, preview string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got.path, got.chat, got.text, got.preview = r.URL.Path, r.Form.Get("chat_id"), r.Form.Get("text"), r.Form.Get("disable_web_page_preview")
		if r.Form.Get("chat_id") == "-1" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()
	old := APIBase
	APIBase = srv.URL
	defer func() { APIBase = old }()

	if err := Send(context.Background(), token, "4242", "Anna's phone is online"); err != nil {
		t.Fatal(err)
	}
	if got.path != "/bot"+token+"/sendMessage" || got.chat != "4242" || got.text != "Anna's phone is online" || got.preview != "true" {
		t.Errorf("request = %+v", got)
	}
	err := Send(context.Background(), token, "-1", "x")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("error = %v", err)
	}
	// An unreachable API: the error must not contain the token.
	APIBase = "http://127.0.0.1:1"
	err = Send(context.Background(), token, "4242", "x")
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "AAEh") {
		t.Errorf("error leaks the token or is nil: %v", err)
	}
}

func TestCheck(t *testing.T) {
	if CheckToken(token) != nil || CheckToken("123:abc") == nil || CheckToken("") == nil {
		t.Error("CheckToken")
	}
	chats, err := ParseChats([]string{"4242, -1001234567890", "@my_channel 4242"})
	if err != nil || strings.Join(chats, ",") != "4242,-1001234567890,@my_channel" {
		t.Errorf("ParseChats = %v %v", chats, err)
	}
	for _, bad := range []string{"abc", "@ab", "12 34x"} {
		if _, err := ParseChats([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
