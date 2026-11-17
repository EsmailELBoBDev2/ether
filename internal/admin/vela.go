package admin

import "net/http"

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
}
