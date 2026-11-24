package vela

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/braedonsaunders/ether/internal/frontend"
)

func (i *instance) screensGeneration() uint64 {
	if gen, ok := i.host.Screens.(frontend.ScreensGeneration); ok {
		return gen.Generation()
	}
	return 0
}

func (i *instance) screenNavigation(r *http.Request, v *frontend.Viewer) ([]any, error) {
	out := []any{}
	if i.host.Screens == nil || v == nil {
		return out, nil
	}
	// A client that draws documents gets document screens; a screen that
	// has no document (or a client that draws none) keeps its sections entry.
	documents := map[string]bool{}
	c := screenClient(r)
	if ps, ok := i.pluginScreens(); ok && c.Renderer > 0 {
		raw, err := ps.ScreenNavigation(r.Context(), v, c)
		if err != nil {
			return nil, err
		}
		var entries []map[string]any
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, err
		}
		for _, e := range entries {
			if id, _ := e["id"].(string); id != "" {
				documents[id] = true
			}
			out = append(out, e)
		}
	}
	nav, err := i.host.Screens.Navigation(r.Context(), v)
	if err != nil {
		return nil, err
	}
	for _, n := range nav {
		if !documents[n.ID] {
			out = append(out, n)
		}
	}
	return out, nil
}

// screenSurfaces lists the surfaces for bootstrap.
func (i *instance) screenSurfaces(r *http.Request, v *frontend.Viewer) (json.RawMessage, error) {
	ps, ok := i.pluginScreens()
	c := screenClient(r)
	if !ok || v == nil || c.Renderer == 0 {
		return json.RawMessage("[]"), nil
	}
	return ps.ScreenSurfaces(r.Context(), v, c)
}

// screen serves GET /vela/v0/screens/{id}?league=&date=.
func (i *instance) screen(w http.ResponseWriter, r *http.Request) {
	v, token, err := i.viewer(r)
	if err != nil {
		fail(w, err, r)
		return
	}
	if i.host.Screens == nil {
		problem(w, 404, "not-found", "not_found")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		problem(w, 404, "not-found", "not_found")
		return
	}
	values := r.URL.Query()
	for name, entries := range values {
		switch name {
		case "league", "date":
			if len(entries) != 1 {
				problem(w, 400, "request", "invalid_request")
				return
			}
		default:
			problem(w, 400, "request", "invalid_request")
			return
		}
	}
	raw, err := i.host.Screens.ResolveScreen(r.Context(), v, id, values.Get("league"), values.Get("date"))
	if err != nil {
		fail(w, err, r)
		return
	}
	signed, err := i.newLiveImages(r, token).signRaw(raw, "")
	if err != nil {
		fail(w, err, r)
		return
	}
	varyLanguage(w)
	cache(w, r, signed)
}
