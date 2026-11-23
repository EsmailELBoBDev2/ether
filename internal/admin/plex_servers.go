package admin

import (
	"context"
	"errors"
	"net/http"
)

// PlexExtras manages extra Plex servers: each is claimed by a different
// plex.tv account and served on the same port as the main server, so viewers
// beyond one account's share limit get seats (config.PlexExtraServer).
type PlexExtras interface {
	// Status lists the servers with their seats and the capacity warning.
	Status(ctx context.Context) any
	// StartPIN begins a plex.tv sign-in for another account.
	StartPIN(ctx context.Context) (id, code, authURL string, err error)
	// PollPIN reports whether the sign-in finished; when it did, the account
	// is stored as a standby server and server describes it.
	PollPIN(ctx context.Context, id string) (done bool, server any, err error)
	// Start claims and publishes a standby server now.
	Start(ctx context.Context, id string) error
	// Rename sets a server's name; empty inherits the main server's name.
	Rename(id, name string) error
	// Remove signs a server out of plex.tv and forgets it.
	Remove(ctx context.Context, id string) error
}

// ErrPlexAccountInUse marks a sign-in with an account that already owns one
// of the servers.
var ErrPlexAccountInUse = errors.New("plex account already used")

func (h *Handler) plexExtras(w http.ResponseWriter) (PlexExtras, bool) {
	if h.deps.PlexExtras == nil {
		writeError(w, http.StatusNotImplemented, "unavailable", "Extra Plex servers are not available on this server.")
		return nil, false
	}
	return h.deps.PlexExtras, true
}

func (h *Handler) handlePlexExtras(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.plexExtras(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, ps.Status(r.Context()))
}

func (h *Handler) handlePlexExtraPINStart(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.plexExtras(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.deps.TestTimeout)
	defer cancel()
	id, code, authURL, err := ps.StartPIN(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "plex_pin_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "code": code, "authUrl": authURL})
}

func (h *Handler) handlePlexExtraPINPoll(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.plexExtras(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.deps.TestTimeout)
	defer cancel()
	done, server, err := ps.PollPIN(ctx, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrPlexAccountInUse) {
			writeError(w, http.StatusConflict, "plex_account_in_use", err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "plex_pin_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"done": done, "server": server})
}

func (h *Handler) handlePlexExtraStart(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.plexExtras(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*h.deps.TestTimeout)
	defer cancel()
	if err := ps.Start(ctx, r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadGateway, "plex_server_start_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ps.Status(r.Context()))
}

func (h *Handler) handlePlexExtraRename(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.plexExtras(w)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if err := ps.Rename(r.PathValue("id"), body.Name); err != nil {
		writeError(w, http.StatusBadRequest, "plex_server_rename_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ps.Status(r.Context()))
}

func (h *Handler) handlePlexExtraRemove(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.plexExtras(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.deps.TestTimeout)
	defer cancel()
	if err := ps.Remove(ctx, r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadGateway, "plex_server_remove_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ps.Status(r.Context()))
}
