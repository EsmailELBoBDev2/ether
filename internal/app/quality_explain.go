package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/clientreg"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend/kit/caps"
	"github.com/braedonsaunders/ether/internal/frontends/plex"
	"github.com/braedonsaunders/ether/internal/resolver"
)

// qualityClient is a known app the owner can ask "who gets what" about.
// The identities are what each official app sends, so the capability
// registry and quality rules match them exactly as they match playback.
type qualityClient struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Product  string        `json:"product"`
	Platform string        `json:"platform"`
	Device   string        `json:"device,omitempty"`
	Protocol core.Protocol `json:"protocol,omitempty"`
}

func qualityClients() []qualityClient {
	return []qualityClient{
		{ID: "plex-apple-tv", Name: "Plex for Apple TV", Product: "Plex for Apple TV", Platform: "tvOS", Device: "Apple TV"},
		{ID: "plex-ios", Name: "Plex for iPhone and iPad", Product: "Plex for iOS", Platform: "iOS", Device: "iPhone"},
		{ID: "plex-android-tv", Name: "Plex for Android TV", Product: "Plex for Android (TV)", Platform: "Android", Device: "Android TV"},
		{ID: "plex-shield", Name: "Plex on NVIDIA Shield", Product: "Plex for Android (TV)", Platform: "Android", Device: "SHIELD Android TV"},
		{ID: "plex-fire-tv", Name: "Plex for Fire TV", Product: "Plex for Android (TV)", Platform: "Android", Device: "AFTKRT"},
		{ID: "plex-android", Name: "Plex for Android phones", Product: "Plex for Android (Mobile)", Platform: "Android", Device: "Pixel 8"},
		{ID: "plex-roku", Name: "Plex for Roku", Product: "Plex for Roku", Platform: "Roku"},
		{ID: "plex-lg", Name: "Plex for LG", Product: "Plex for LG", Platform: "webOS"},
		{ID: "plex-samsung", Name: "Plex for Samsung", Product: "Plex for Samsung", Platform: "Tizen"},
		{ID: "plex-xbox", Name: "Plex for Xbox", Product: "Plex for Xbox", Platform: "Xbox"},
		{ID: "plex-playstation", Name: "Plex for PlayStation", Product: "Plex for PlayStation", Platform: "PlayStation"},
		{ID: "plex-web-chrome", Name: "Plex Web in Chrome", Product: "Plex Web", Platform: "Chrome"},
		{ID: "plex-web-safari", Name: "Plex Web in Safari", Product: "Plex Web", Platform: "Safari"},
		{ID: "plex-htpc", Name: "Plex HTPC", Product: "Plex HTPC", Platform: "Windows"},
		{ID: "plex-kodi", Name: "Plex for Kodi", Product: "PM4K", Device: "Kodi"},
		{ID: "plex-desktop", Name: "Plex for Windows and Mac", Product: "Plex for Windows", Platform: "Windows"},
		{ID: "jellyfin", Name: "Jellyfin", Product: "Jellyfin", Protocol: "jellyfin"},
		{ID: "emby", Name: "Emby", Product: "Emby", Protocol: "emby"},
	}
}

type qualityDeviceApp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
}

type qualityDeviceClient struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Group    string    `json:"group,omitempty"`
	Type     string    `json:"type,omitempty"`
	App      string    `json:"app,omitempty"`
	Viewer   string    `json:"viewer,omitempty"`
	LastSeen time.Time `json:"lastSeen"`
}

type qualityDevicesResponse struct {
	Groups  []clientreg.ClassInfo      `json:"groups"`
	Types   []clientreg.DeviceTypeInfo `json:"types"`
	Apps    []qualityDeviceApp         `json:"apps"`
	Clients []qualityDeviceClient      `json:"clients"`
}

type qualityExplainIn struct {
	UserID    int64          `json:"userId"`
	ClientID  string         `json:"clientId,omitempty"`
	Client    *qualityClient `json:"client,omitempty"`
	Remote    bool           `json:"remote"`
	TitleKey  string         `json:"titleKey,omitempty"`
	Kind      string         `json:"kind,omitempty"`
	LibraryID int64          `json:"libraryId,omitempty"`
}

// qualityHandler serves the Releases page's device picker, summary table and
// "Check one play" explainer.
func (a *App) qualityHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /clients", func(w http.ResponseWriter, r *http.Request) {
		writeQualityJSON(w, http.StatusOK, map[string]any{"clients": qualityClients()})
	})
	m.HandleFunc("GET /devices", a.handleQualityDevices)
	m.HandleFunc("GET /summary", a.handleQualitySummary)
	m.HandleFunc("POST /explain", a.handleQualityExplain)
	return m
}

func (a *App) handleQualityDevices(w http.ResponseWriter, r *http.Request) {
	groups := clientreg.Classes()
	types := clientreg.DeviceTypes()
	regApps := clientreg.Apps()
	apps := make([]qualityDeviceApp, 0, len(regApps))
	for _, item := range regApps {
		apps = append(apps, qualityDeviceApp{
			ID:       clientreg.AppID(item),
			Name:     item.Name,
			Protocol: item.Protocol,
		})
	}
	clients := []qualityDeviceClient{}
	if a.db != nil {
		if seen, err := a.seenPlaybackDevices(r.Context()); err == nil {
			clients = make([]qualityDeviceClient, 0, len(seen))
			for _, d := range seen {
				id := clientreg.Identity{
					Product:    d.App,
					Platform:   d.Platform,
					Device:     d.Device,
					DeviceName: d.Name,
				}
				name := strings.TrimSpace(d.Name)
				if name == "" {
					name = strings.TrimSpace(d.App)
				}
				if name == "" {
					name = strings.TrimSpace(d.Device)
				}
				if name == "" {
					name = d.ClientIdentifier
				}
				clients = append(clients, qualityDeviceClient{
					ID:       d.ClientIdentifier,
					Name:     name,
					Group:    clientreg.Class(id),
					Type:     clientreg.DeviceType(id),
					App:      clientreg.AppFor("", id),
					Viewer:   d.Viewer,
					LastSeen: d.LastSeen,
				})
			}
		}
	}
	writeQualityJSON(w, http.StatusOK, qualityDevicesResponse{
		Groups:  groups,
		Types:   types,
		Apps:    apps,
		Clients: clients,
	})
}

func (a *App) pluginDisplayName(ctx context.Context, pluginID string) string {
	if pluginID == "" {
		return ""
	}
	if a.pluginsRT != nil && a.pluginsRT.mgr != nil {
		if rec, err := a.pluginsRT.mgr.Get(ctx, pluginID); err == nil && strings.TrimSpace(rec.Name) != "" {
			return rec.Name
		}
	}
	return pluginID
}

func (a *App) handleQualitySummary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var viewerID int64
	if rawViewer := strings.TrimSpace(q.Get("viewer")); rawViewer != "" {
		id, err := strconv.ParseInt(rawViewer, 10, 64)
		if err != nil || id <= 0 {
			writeQualityError(w, http.StatusBadRequest, "bad_viewer", "viewer must be a positive viewer id")
			return
		}
		viewerID = id
	}
	device, ok := resolver.ParseSummaryDevice(q.Get("device"))
	if !ok {
		writeQualityError(w, http.StatusBadRequest, "bad_device", "device must be <field>:<id>")
		return
	}
	if device.Field == "client" && a.db != nil {
		if seen, err := a.seenPlaybackDevices(r.Context()); err == nil {
			for _, d := range seen {
				if d.ClientIdentifier == device.Value {
					id := clientreg.Identity{Product: d.App, Platform: d.Platform, Device: d.Device, DeviceName: d.Name}
					device.ClientGroup = clientreg.Class(id)
					device.ClientType = clientreg.DeviceType(id)
					device.ClientApp = clientreg.AppFor("", id)
					device.ClientCaps = core.ClientCaps{
						ClientID:   d.ClientIdentifier,
						Product:    d.App,
						Platform:   d.Platform,
						Device:     d.Device,
						DeviceName: d.Name,
					}
					break
				}
			}
		}
	}
	var sumLibs []resolver.SummaryLibrary
	if a.libraries != nil {
		libs, err := a.libraries.List(r.Context())
		if err != nil {
			writeQualityError(w, http.StatusInternalServerError, "libraries_failed", "could not list libraries")
			return
		}
		sumLibs = make([]resolver.SummaryLibrary, 0, len(libs))
		for _, lib := range libs {
			pluginName := ""
			chosenByPlugin := false
			if lib.Source == core.LibraryPlugin {
				pluginName = a.pluginDisplayName(r.Context(), lib.PluginID)
				chosenByPlugin = true
			}
			sumLibs = append(sumLibs, resolver.SummaryLibrary{
				ID:             int64(lib.ID),
				Name:           lib.Name,
				MediaType:      lib.MediaType,
				Plugin:         pluginName,
				ChosenByPlugin: chosenByPlugin,
			})
		}
	}
	cfg := a.cfg.Read()
	writeQualityJSON(w, http.StatusOK, resolver.SummarizeQuality(cfg.Quality, sumLibs, viewerID, device))
}

func (a *App) handleQualityExplain(w http.ResponseWriter, r *http.Request) {
	var in qualityExplainIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		writeQualityError(w, http.StatusBadRequest, "bad_json", "the request body must be a JSON object")
		return
	}
	if in.Kind != "" && in.Kind != config.KindMovie && in.Kind != config.KindEpisode {
		writeQualityError(w, http.StatusBadRequest, "bad_kind", "kind must be movie or episode")
		return
	}
	var client qualityClient
	var exactClientID string
	switch {
	case in.ClientID == "any" || (in.ClientID == "" && in.Client == nil):
		// Generic client with no assumed app.
	case in.ClientID != "":
		found := false
		// First check seen devices (exact device id from /quality/devices).
		if a.db != nil {
			if seen, err := a.seenPlaybackDevices(r.Context()); err == nil {
				for _, d := range seen {
					if d.ClientIdentifier == in.ClientID {
						exactClientID = d.ClientIdentifier
						client = qualityClient{
							ID:       d.ClientIdentifier,
							Name:     d.Name,
							Product:  d.App,
							Platform: d.Platform,
							Device:   d.Device,
						}
						found = true
						break
					}
				}
			}
		}
		if !found {
			for _, c := range qualityClients() {
				if c.ID == in.ClientID {
					client, found = c, true
					break
				}
			}
		}
		if !found {
			// Accept "<field>:<id>" from /quality/devices as well.
			if dev, ok := resolver.ParseSummaryDevice(in.ClientID); ok && dev.Field != "" {
				switch dev.Field {
				case "type":
					for _, d := range clientreg.Devices() {
						if d.Name == dev.Value {
							client = qualityClient{ID: in.ClientID, Name: d.Name, Product: d.Identity.Product, Platform: d.Identity.Platform, Device: d.Identity.Device}
							found = true
							break
						}
					}
				case "app":
					for _, appItem := range clientreg.Apps() {
						if clientreg.AppID(appItem) == dev.Value {
							prod := appItem.Identity.Product
							if prod == "" {
								prod = appItem.Product
							}
							if prod == "" {
								prod = appItem.Name
							}
							client = qualityClient{ID: in.ClientID, Name: appItem.Name, Product: prod, Platform: appItem.Identity.Platform, Device: appItem.Identity.Device, Protocol: core.Protocol(appItem.Protocol)}
							found = true
							break
						}
					}
				}
			}
		}
		if !found {
			writeQualityError(w, http.StatusBadRequest, "unknown_client", "no such device or app")
			return
		}
	case in.Client != nil && strings.TrimSpace(in.Client.Product) != "":
		client = qualityClient{Product: strings.TrimSpace(in.Client.Product), Platform: strings.TrimSpace(in.Client.Platform), Device: strings.TrimSpace(in.Client.Device), Protocol: in.Client.Protocol}
	default:
		writeQualityError(w, http.StatusBadRequest, "missing_client", "choose a device or app")
		return
	}
	if len(client.Product) > 128 || len(client.Platform) > 128 || len(client.Device) > 128 {
		writeQualityError(w, http.StatusBadRequest, "bad_client", "app fields are too long")
		return
	}
	var u *core.User
	if in.UserID > 0 {
		loaded, err := a.users.ActiveUser(r.Context(), in.UserID)
		if err != nil || loaded == nil {
			writeQualityError(w, http.StatusNotFound, "unknown_viewer", "no such viewer")
			return
		}
		u = loaded
	}
	var title *core.Title
	var inLib resolver.Membership
	var chosenByPlugin string
	kind := in.Kind
	if in.TitleKey != "" {
		key, err := core.ParseRatingKey(in.TitleKey)
		if err != nil {
			writeQualityError(w, http.StatusBadRequest, "bad_title", "invalid title")
			return
		}
		loaded, err := a.catalog.CachedGet(r.Context(), key)
		if err != nil || loaded == nil || (loaded.Type != core.TypeMovie && loaded.Type != core.TypeEpisode) {
			writeQualityError(w, http.StatusNotFound, "unknown_title", "no such movie or episode")
			return
		}
		title = loaded
		if a.libProfiles != nil {
			inLib = func(id core.LibraryID) bool {
				return a.libProfiles.InLibrary(r.Context(), title, id)
			}
		}
	} else if in.LibraryID > 0 {
		if a.libraries == nil {
			writeQualityError(w, http.StatusNotFound, "unknown_library", "no such library")
			return
		}
		lib, err := a.libraries.Get(r.Context(), core.LibraryID(in.LibraryID))
		if err != nil {
			writeQualityError(w, http.StatusNotFound, "unknown_library", "no such library")
			return
		}
		inLib = func(id core.LibraryID) bool { return id == lib.ID }
		if lib.Source == core.LibraryPlugin {
			chosenByPlugin = a.pluginDisplayName(r.Context(), lib.PluginID)
		}
		if kind == "" {
			switch lib.MediaType {
			case core.TypeMovie:
				kind = config.KindMovie
			case core.TypeShow:
				kind = config.KindEpisode
			}
		}
	}
	cfg := a.cfg.Read()
	// The Plex request path: registry, owner App compatibility edits and request
	// headers (remote bitrate caps now live in quality rules).
	devName := client.Name
	if devName == "" {
		devName = client.Device
	}
	pr := &plex.Request{ClientID: exactClientID, Product: client.Product, Platform: client.Platform, Device: client.Device, DeviceName: devName, Remote: in.Remote, User: u}
	declared := pr.ClientCapsWith(0, cfg.Clients.Overrides)
	builtin := pr.ClientCapsWith(0, nil)
	if frontendHostPolicy(client.Protocol).ProfileQualityEvaluation {
		info := caps.DeviceInfo{Protocol: client.Protocol, ClientID: exactClientID, Product: client.Product, Platform: client.Platform, Device: client.Device, Remote: in.Remote}
		neutral, err := (caps.Builder{}).Build(r.Context(), info, caps.DeviceProfile{}, nil, "")
		if err != nil {
			writeQualityError(w, http.StatusBadRequest, "bad_client", "cannot evaluate app")
			return
		}
		builtin = playbackFloor(neutral, nil)
		declared = playbackFloor(neutral, cfg.Clients.Overrides)
	} else if client.Product == "" {
		declared = core.ClientCaps{ClientID: exactClientID, Remote: in.Remote, HDR10: true, DolbyVision: true}
		builtin = declared
	}

	q := resolver.QualityQuestion{
		Quality:        cfg.Quality,
		Caps:           declared,
		Builtin:        &builtin,
		User:           u,
		Title:          title,
		Kind:           kind,
		InLibrary:      inLib,
		ChosenByPlugin: chosenByPlugin,
	}
	if a.safety != nil && u != nil {
		policy := a.safety.ForUser(u)
		q.RemoteDisabled = policy.RemoteDisabled
	}
	writeQualityJSON(w, http.StatusOK, resolver.ExplainQuality(q))
}

func writeQualityJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeQualityError(w http.ResponseWriter, code int, kind, message string) {
	writeQualityJSON(w, code, map[string]any{"error": map[string]string{"code": kind, "message": message}})
}
