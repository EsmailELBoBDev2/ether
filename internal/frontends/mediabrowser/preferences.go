package mediabrowser

import (
	"encoding/json"
	"errors"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/kit/route"
	"io"
	"net/http"
	"strings"
)

func (i *instance) displayPreferences(w http.ResponseWriter, r *http.Request) {
	v, _, err := i.viewer(r)
	if err != nil || v == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}
	if i.host.Preferences == nil {
		http.Error(w, "Unavailable", 503)
		return
	}
	client := route.Query(r.URL.Query(), "client")
	key := r.PathValue("id")
	if len(client) != 1 || client[0] == "" || len(client[0]) > 128 || len(key) > 128 {
		http.Error(w, "Bad Request", 400)
		return
	}
	if r.Method == "POST" {
		body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
		if e != nil || !json.Valid(body) {
			http.Error(w, "Bad Request", 400)
			return
		}
		var object map[string]any
		if json.Unmarshal(body, &object) != nil || object == nil {
			http.Error(w, "Bad Request", 400)
			return
		}
		if e = i.host.Preferences.Put(r.Context(), v, client[0], key, body); e != nil {
			http.Error(w, "Unavailable", 503)
			return
		}
		w.WriteHeader(204)
		return
	}
	body, err := i.host.Preferences.Get(r.Context(), v, client[0], key)
	if errors.Is(err, frontend.ErrPreferencesNotFound) {
		i.writeDisplayPreferences(w, r, v, key, client[0], nil)
		return
	}
	if err != nil {
		http.Error(w, "Unavailable", 503)
		return
	}
	i.writeDisplayPreferences(w, r, v, key, client[0], body)
}

// Jellyfin's DisplayPreferences constructor fills these fields even when the
// viewer has never saved a document. CustomPrefs stay empty: those keys are
// client-written settings, not server defaults.
func jellyfinDisplayDefaults(id, client string) map[string]any {
	return map[string]any{
		"Id": id, "Client": client, "CustomPrefs": map[string]any{},
		"ScrollDirection": "Horizontal", "SortOrder": "Ascending", "SortBy": "SortName",
		"RememberIndexing": false, "RememberSorting": false, "ShowBackdrop": true, "ShowSidebar": false,
		"PrimaryImageHeight": 250, "PrimaryImageWidth": 250,
	}
}

func (i *instance) writeDisplayPreferences(w http.ResponseWriter, r *http.Request, v *frontend.Viewer, id, client string, body []byte) {
	if i.protocol != "jellyfin" {
		object := map[string]any{}
		if body != nil && (json.Unmarshal(body, &object) != nil || object == nil) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write(body)
			return
		}
		if body == nil {
			object = map[string]any{"Id": id, "Client": client, "CustomPrefs": map[string]any{}, "ScrollDirection": "Horizontal", "SortOrder": "Ascending"}
		}
		if _, ok := object["CustomPrefs"]; !ok {
			object["CustomPrefs"] = map[string]any{}
		}
		i.applyHomeSectionDefaults(r, v, id, object)
		jsonReply(w, object)
		return
	}
	object := map[string]any{}
	if body != nil && (json.Unmarshal(body, &object) != nil || object == nil) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(body)
		return
	}
	for key, value := range jellyfinDisplayDefaults(id, client) {
		if _, ok := object[key]; !ok {
			object[key] = value
		}
	}
	if prefs, ok := object["CustomPrefs"].(map[string]any); !ok || prefs == nil {
		object["CustomPrefs"] = map[string]any{}
	}
	i.applyHomeSectionDefaults(r, v, id, object)
	jsonReply(w, object)
}

// userSettings answers /usersettings/{userId} and its /Partial update, which
// Emby Web uses instead of DisplayPreferences on a 4.9+ server. They are the
// CustomPrefs of the same stored document ("usersettings", client "emby") the
// older endpoint reads, as a flat object.
func (i *instance) userSettings(w http.ResponseWriter, r *http.Request) {
	v, _, err := i.viewer(r)
	if err != nil || v == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}
	if i.host.Preferences == nil {
		http.Error(w, "Unavailable", 503)
		return
	}
	const client, key = "emby", "usersettings"
	stored := map[string]any{}
	if body, e := i.host.Preferences.Get(r.Context(), v, client, key); e == nil {
		if json.Unmarshal(body, &stored) != nil || stored == nil {
			stored = map[string]any{}
		}
	} else if !errors.Is(e, frontend.ErrPreferencesNotFound) {
		http.Error(w, "Unavailable", 503)
		return
	}
	prefs, _ := stored["CustomPrefs"].(map[string]any)
	if prefs == nil {
		prefs = map[string]any{}
	}
	if r.Method != http.MethodPost {
		object := map[string]any{"Id": key, "Client": client, "CustomPrefs": prefs}
		i.applyHomeSectionDefaults(r, v, key, object)
		out, _ := object["CustomPrefs"].(map[string]any)
		if out == nil {
			out = map[string]any{}
		}
		jsonReply(w, out)
		return
	}
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
	var update map[string]any
	if e != nil || json.Unmarshal(body, &update) != nil || update == nil {
		http.Error(w, "Bad Request", 400)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/Partial") || strings.HasSuffix(r.URL.Path, "/partial") {
		for k, val := range update {
			prefs[k] = val
		}
	} else {
		prefs = update
	}
	stored["Id"], stored["Client"], stored["CustomPrefs"] = key, client, prefs
	doc, _ := json.Marshal(stored)
	if e = i.host.Preferences.Put(r.Context(), v, client, key, doc); e != nil {
		http.Error(w, "Unavailable", 503)
		return
	}
	w.WriteHeader(204)
}
