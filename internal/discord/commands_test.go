package discord

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterCommands_IncludesRead(t *testing.T) {
	var captured []slashCommand
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("failed to parse request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := RegisterCommands(srv.URL, "app-id", "bot-token", "backflow"); err != nil {
		t.Fatalf("RegisterCommands error: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("commands len = %d, want 1", len(captured))
	}
	subs := map[string]slashCommandOption{}
	for _, opt := range captured[0].Options {
		subs[opt.Name] = opt
	}
	read, ok := subs["read"]
	if !ok {
		t.Fatalf("read subcommand not registered; got subcommands: %v", keys(subs))
	}
	if read.Type != 1 {
		t.Errorf("read subcommand type = %d, want 1 (SUB_COMMAND)", read.Type)
	}
	// Required: url (string, type 3). Optional: force (boolean, type 5).
	var urlOpt, forceOpt *slashCommandOption
	for i := range read.Options {
		opt := &read.Options[i]
		switch opt.Name {
		case "url":
			urlOpt = opt
		case "force":
			forceOpt = opt
		}
	}
	if urlOpt == nil {
		t.Fatalf("read.url option missing")
	}
	if urlOpt.Type != 3 {
		t.Errorf("url.type = %d, want 3 (STRING)", urlOpt.Type)
	}
	if !urlOpt.Required {
		t.Errorf("url.required = false, want true")
	}
	if forceOpt == nil {
		t.Fatalf("read.force option missing")
	}
	if forceOpt.Type != 5 {
		t.Errorf("force.type = %d, want 5 (BOOLEAN)", forceOpt.Type)
	}
	if forceOpt.Required {
		t.Errorf("force.required = true, want false")
	}

	// Sanity: other subcommands still present.
	for _, name := range []string{"create", "status", "list", "cancel", "retry"} {
		if _, ok := subs[name]; !ok {
			t.Errorf("existing subcommand %q missing after adding read", name)
		}
	}
}

func keys(m map[string]slashCommandOption) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
