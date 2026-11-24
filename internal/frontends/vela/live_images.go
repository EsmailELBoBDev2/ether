package vela

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/braedonsaunders/ether/internal/frontend"
)

// liveImages rewrites the image URLs a plugin put in live-event payloads
// (league logos, competitor and entity crests) into signed Ether image grants,
// and channel logos on watch targets into the channel's own grant. A raw
// upstream URL never reaches the client: a logo Ether cannot sign is dropped.
//
// One signer serves one response (or one stream), so a crest repeated across a
// screen is signed once.
type liveImages struct {
	i     *instance
	r     *http.Request
	token string
	// mu guards issued: one signer can be shared by concurrent readers of one
	// response (screen data sources resolve in parallel).
	mu     sync.Mutex
	issued map[string]string
}

func (i *instance) newLiveImages(r *http.Request, token string) *liveImages {
	return &liveImages{i: i, r: r, token: token, issued: map[string]string{}}
}

func (s *liveImages) plugin(pluginID, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "/vela/v0/images/") {
		return raw
	}
	key := "p\x00" + pluginID + "\x00" + raw
	s.mu.Lock()
	defer s.mu.Unlock()
	if got, ok := s.issued[key]; ok {
		return got
	}
	out := ""
	if img, ok := frontend.PluginImage(pluginID, raw); ok && s.i.host.ImageGrants != nil {
		secret, err := s.i.host.ImageGrants.Issue(s.r.Context(), frontend.ImageGrantRequest{Credential: s.token, Device: requestDevice(s.r), Image: img})
		if err == nil && secret != "" {
			out = "/vela/v0/images/" + secret
		}
	}
	s.issued[key] = out
	return out
}

func (s *liveImages) channel(channelID string) string {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return ""
	}
	key := "c\x00" + channelID
	s.mu.Lock()
	defer s.mu.Unlock()
	if got, ok := s.issued[key]; ok {
		return got
	}
	out := ""
	if issuer, ok := s.i.host.ImageGrants.(frontend.ChannelLogoIssuer); ok {
		secret, err := issuer.IssueChannelLogo(s.r.Context(), s.token, requestDevice(s.r), channelID)
		if err == nil && secret != "" {
			out = "/vela/v0/images/" + secret
		}
	}
	s.issued[key] = out
	return out
}

// sign marshals v and rewrites every "logo" in it. pluginID is the plugin the
// payload belongs to when the payload itself does not say (a screen's
// top-level pluginId, an event's namespaced id or an entity's key say so).
func (s *liveImages) sign(v any, pluginID string) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return s.signRaw(b, pluginID)
}

func (s *liveImages) signRaw(b []byte, pluginID string) (json.RawMessage, error) {
	if !bytes.Contains(b, []byte(`"logo"`)) {
		return b, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	root = s.walk(root, pluginID, "")
	return json.Marshal(root)
}

func (s *liveImages) walk(node any, pluginID, parentKey string) any {
	switch n := node.(type) {
	case []any:
		for idx, child := range n {
			n[idx] = s.walk(child, pluginID, parentKey)
		}
		return n
	case map[string]any:
		pluginID = objectPlugin(n, pluginID)
		watchTarget := parentKey == "watch" || parentKey == "alternates"
		for k, child := range n {
			if k == "logo" {
				raw, _ := child.(string)
				var signed string
				if watchTarget {
					// A watch target's logo is its channel's; anything else
					// (an add-on or plugin target) carries no logo.
					channelID, _ := n["channelId"].(string)
					if raw != "" {
						signed = s.channel(channelID)
					}
				} else if pluginID != "" {
					signed = s.plugin(pluginID, raw)
				}
				if signed == "" {
					delete(n, k)
				} else {
					n[k] = signed
				}
				continue
			}
			n[k] = s.walk(child, pluginID, k)
		}
		return n
	default:
		return node
	}
}

// objectPlugin reads the owning plugin from an object that names it: a
// resolved screen's pluginId, an entity's favourites key, or a live event's
// namespaced id ("pluginID:feedID:eventID").
func objectPlugin(n map[string]any, fallback string) string {
	if p, ok := n["pluginId"].(string); ok && strings.TrimSpace(p) != "" {
		return strings.TrimSpace(p)
	}
	if key, ok := n["key"].(string); ok {
		// "pluginID:feedID:entityKind:entityID"
		if parts := strings.SplitN(key, ":", 4); len(parts) == 4 && parts[0] != "" && parts[3] != "" {
			return parts[0]
		}
	}
	if id, ok := n["id"].(string); ok && strings.Count(id, ":") >= 2 {
		_, hasCompetitors := n["competitors"]
		_, hasStatus := n["status"]
		if hasCompetitors || hasStatus {
			if p, _, ok := strings.Cut(id, ":"); ok && p != "" {
				return p
			}
		}
	}
	return fallback
}
