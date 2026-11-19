package admin

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
)

// VelaWordmark is the response returned when an owner uploads a Vela wordmark image.
type VelaWordmark struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Digest string `json:"digest"`
}

// ErrVelaWordmarkTooLarge indicates an uploaded Vela wordmark exceeds 512 KB.
var ErrVelaWordmarkTooLarge = errors.New("wordmark exceeds 512 KB")

// VelaWordmarkValidationError indicates an uploaded Vela wordmark failed format or dimension checks.
type VelaWordmarkValidationError struct {
	Message string
}

func (e *VelaWordmarkValidationError) Error() string { return e.Message }

// VelaWordmarkUploader is an optional capability on FrontendAdmin for persisting Vela wordmarks.
type VelaWordmarkUploader interface {
	UploadVelaWordmark(ctx context.Context, r io.Reader) (VelaWordmark, error)
}

func (h *Handler) velaRoutes(api func(string, http.HandlerFunc)) {
	for _, action := range []string{"status", "check", "install", "rollback"} {
		method := "POST"
		if action == "status" {
			method = "GET"
		}
		api(method+" /api/vela/"+action, func(w http.ResponseWriter, r *http.Request) {
			// Setup-open access is not sufficient: distribution actions require an owner.
			if !h.isOwner(r, tokenFromRequest(r)) {
				writeError(w, 403, "forbidden", "owner sign-in required")
				return
			}
			m := h.deps.Vela
			if m == nil {
				writeError(w, 503, "unavailable", "Vela distribution unavailable")
				return
			}
			var err error
			switch action {
			case "check":
				err = m.Check(r.Context())
			case "install":
				err = m.Install(r.Context())
			case "rollback":
				err = m.Rollback(r.Context())
			}
			if err != nil {
				writeError(w, 409, "vela_action_failed", err.Error())
				return
			}
			writeJSON(w, 200, m.Status())
		})
	}
	api("POST /api/frontends/vela/wordmark", h.handleVelaWordmark)
}

func (h *Handler) handleVelaWordmark(w http.ResponseWriter, r *http.Request) {
	if !h.isOwner(r, tokenFromRequest(r)) {
		writeError(w, http.StatusForbidden, "forbidden", "owner sign-in required")
		return
	}
	upload := h.deps.UploadVelaWordmark
	if upload == nil {
		if u, ok := h.deps.Frontends.(VelaWordmarkUploader); ok {
			upload = u.UploadVelaWordmark
		}
	}
	if upload == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "wordmark storage unavailable")
		return
	}
	// Bound total upload envelope (512 KB image + multipart framing).
	r.Body = http.MaxBytesReader(w, r.Body, (512<<10)+(64<<10))
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "multipart/form-data" {
		mr, err := r.MultipartReader()
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_upload", "send the wordmark as a multipart file field")
			return
		}
		for {
			part, err := mr.NextPart()
			if err != nil {
				var maxErr *http.MaxBytesError
				if errors.As(err, &maxErr) {
					writeError(w, http.StatusRequestEntityTooLarge, "too_large", "wordmark must be 512 KB or smaller")
					return
				}
				writeError(w, http.StatusBadRequest, "bad_upload", "the upload has no file field")
				return
			}
			if part.FormName() != "file" {
				_ = part.Close()
				continue
			}
			wm, err := upload(r.Context(), part)
			_ = part.Close()
			if err != nil {
				h.writeVelaWordmarkError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, wm)
			return
		}
	}
	wm, err := upload(r.Context(), r.Body)
	if err != nil {
		h.writeVelaWordmarkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wm)
}

func (h *Handler) writeVelaWordmarkError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	var valErr *VelaWordmarkValidationError
	switch {
	case errors.Is(err, ErrVelaWordmarkTooLarge) || errors.As(err, &maxErr):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "wordmark must be 512 KB or smaller")
	case errors.As(err, &valErr):
		writeError(w, http.StatusBadRequest, "invalid_wordmark", valErr.Message)
	default:
		h.log.Warn("vela wordmark upload", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "the wordmark upload failed")
	}
}
