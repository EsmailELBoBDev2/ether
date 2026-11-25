package newznab

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
	"github.com/braedonsaunders/ether/internal/vault"
)

const pluginNZBSeal = "plugin-indexer-release/v1"

var errPluginNZB = fmt.Errorf("indexer NZB unavailable: %w", core.ErrSourceUnavailable)

// IsIndexerHost is evaluated against the current configuration on every call.
func (m *Manager) IsIndexerHost(host string) bool {
	for _, c := range m.Clients() {
		u, err := url.Parse(c.Config().URL)
		if err == nil && strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), strings.TrimSuffix(host, ".")) {
			return true
		}
	}
	return false
}

type pluginNZBReference struct {
	Key       vault.Key
	ID        string
	GUID      string
	IndexerID string
	Link      string
	Name      string
	Size      int64
	Category  int
	ContentID string
}

func (m *Manager) sealNZBReference(ref pluginNZBReference) string {
	if m.opts.PluginReferences == nil {
		return ""
	}
	data, err := json.Marshal(ref)
	if err != nil {
		return ""
	}
	sealed, err := m.opts.PluginReferences.Seal(pluginNZBSeal, data)
	if err != nil {
		return ""
	}
	return sealed
}

// PluginNZBReference issues a portable, authenticated reference from a real
// search result. Credential-bearing links stay inside the sealed envelope.
func (m *Manager) PluginNZBReference(r core.Release) string {
	if r.Protocol == core.ProtocolTorrent || r.Protocol == core.ProtocolHTTP {
		return ""
	}
	key := vault.KeyOf(r)
	if key == "" && (r.IndexerID == "" || (r.NZBURL == "" && r.GUID == "")) {
		return ""
	}
	return m.sealNZBReference(pluginNZBReference{
		Key: key, ID: r.ID, GUID: r.GUID, IndexerID: r.IndexerID,
		Link: m.StripLinkAuth(r).NZBURL, Name: r.Name, Size: r.Size,
		Category: r.Category, ContentID: r.ContentID,
	})
}

// RequestPluginNZB resolves a search reference, a native indexer/GUID
// identity, or a legacy download link through the ordinary grab client.
func (m *Manager) RequestPluginNZB(ctx context.Context, ref string, hint core.Release) ([]byte, string, error) {
	if m.opts.Vault == nil || m.opts.PluginReferences == nil {
		return nil, "", errPluginNZB
	}
	var key vault.Key
	var data []byte
	var storedAlready bool
	var err error
	switch {
	case ref != "":
		if len(ref) > 32768 {
			return nil, "", errPluginNZB
		}
		decoded, decodeErr := m.opts.PluginReferences.Unseal(pluginNZBSeal, ref)
		var stored pluginNZBReference
		if decodeErr != nil || json.Unmarshal(decoded, &stored) != nil {
			return nil, "", errPluginNZB
		}
		key = stored.Key
		storedAlready = key != ""
		hint = core.Release{ID: stored.ID, GUID: stored.GUID, IndexerID: stored.IndexerID,
			NZBURL: stored.Link, Name: stored.Name, Size: stored.Size, Category: stored.Category, ContentID: stored.ContentID}
	case hint.NZBURL != "":
		hint, err = m.legacyPluginNZB(hint)
	case hint.IndexerID != "" && hint.GUID != "":
		var c *Client
		c, err = m.releaseClient(hint)
		if err == nil && (c.Config().UsesBackend() || c.Config().IsZilean()) {
			err = errPluginNZB
		}
		hint.ID = releaseID(hint.IndexerID, hint.GUID, "")
	default:
		return nil, "", errPluginNZB
	}
	if key == "" && err == nil && hint.IndexerID != "" && hint.ID != "" {
		var found bool
		key, found, err = m.opts.Vault.ByAlias(ctx, hint.IndexerID, hint.ID)
		if !found {
			key = ""
		}
	}
	if err == nil && key != "" {
		data, err = m.FetchNZB(ctx, core.Release{IndexerID: vault.IndexerID, GUID: "vault:" + string(key)})
	} else if err == nil {
		data, err = m.FetchNZB(ctx, hint)
	}
	if err != nil {
		// Keep classifications used for scheduling and fallback, without
		// returning indexer errors, identifiers, or URLs to a plugin.
		for _, public := range []error{context.Canceled, context.DeadlineExceeded, core.ErrBudgetExhausted, core.ErrReleaseRejected} {
			if errors.Is(err, public) {
				return nil, "", public
			}
		}
		return nil, "", errPluginNZB
	}
	if key == "" {
		doc, parseErr := nzb.Parse(data)
		if parseErr != nil {
			return nil, "", errPluginNZB
		}
		key = vault.ContentKey(doc)
		// A successful central grab must have reached durable storage.
		if _, err := m.opts.Vault.Entry(ctx, key); err != nil {
			return nil, "", errPluginNZB
		}
	}
	storeRef := ref
	if !storedAlready {
		storeRef = m.sealNZBReference(pluginNZBReference{Key: key})
	}
	if storeRef == "" {
		return nil, "", errPluginNZB
	}
	return data, storeRef, nil
}

func (m *Manager) legacyPluginNZB(hint core.Release) (core.Release, error) {
	u, err := url.Parse(hint.NZBURL)
	if err != nil || u.User != nil || u.Fragment != "" || u.Host == "" {
		return core.Release{}, errPluginNZB
	}
	q := u.Query()
	var selected core.Release
	for _, c := range m.Clients() {
		cfg := c.Config()
		if !cfg.Enabled || cfg.UsesBackend() || cfg.IsZilean() || !sameAuthOrigin(cfg.URL, u.String()) {
			continue
		}
		if hint.IndexerID != "" && hint.IndexerID != c.indexerID() {
			continue
		}
		base, err := url.Parse(cfg.URL)
		if err != nil {
			continue
		}
		root := strings.TrimRight(strings.TrimSuffix(base.Path, "/api"), "/")
		var guid string
		var link string
		switch {
		case (u.Path == root+"/api" || u.Path == root+"/api/newznab") && strings.EqualFold(q.Get("t"), "get"):
			guid = q.Get("id")
			link = c.StripLinkAuth(u.String())
		case u.Path == root+"/download":
			guid = q.Get("guid")
			if guid == "" && strings.HasPrefix(cfg.ID, "prowlarr-") {
				if _, err := strconv.Atoi(path.Base(root)); err == nil {
					guid = q.Get("link")
				}
			}
			link = c.StripLinkAuth(u.String())
		case strings.HasPrefix(u.Path, root+"/getnzb/") && path.Dir(u.Path) == root+"/getnzb":
			guid = strings.TrimSuffix(path.Base(u.Path), ".nzb")
			link = c.StripLinkAuth(u.String())
		}
		if guid == "" || len(guid) > 4096 {
			continue
		}
		if selected.IndexerID != "" {
			return core.Release{}, errPluginNZB
		}
		selected = core.Release{ID: releaseID(c.indexerID(), guid, ""), GUID: guid,
			IndexerID: c.indexerID(), NZBURL: link, Name: hint.Name, Size: hint.Size, Category: hint.Category}
	}
	if selected.IndexerID == "" {
		return core.Release{}, errPluginNZB
	}
	return selected, nil
}

// nzbLinkAlias lets an old link and a native search reference share a vault
// record even when the feed GUID differs from the URL's download identifier.
func (c *Client) nzbLinkAlias(link string) string {
	if link == "" {
		return ""
	}
	absolute, err := c.resolveLink(link)
	if err != nil {
		return ""
	}
	u, err := url.Parse(c.StripLinkAuth(absolute))
	if err != nil {
		return ""
	}
	q := u.Query()
	for k := range q {
		if strings.EqualFold(k, "apikey") {
			delete(q, k)
		}
	}
	u.User, u.Fragment, u.RawQuery = nil, "", q.Encode()
	return fmt.Sprintf("link:%x", sha256.Sum256([]byte(u.String())))
}

func (c *Client) nzbRefusal(r core.Release) error {
	c.nzbFlightMu.Lock()
	defer c.nzbFlightMu.Unlock()
	for _, id := range []string{r.ID, c.nzbLinkAlias(r.NZBURL)} {
		if _, blocked := c.nzbStoreFailures[id]; blocked {
			return vault.ErrPayloadMissing
		}
	}
	if c.nzbRefusalDay == c.today() {
		return c.nzbRefusals[r.ID]
	}
	return nil
}

func (c *Client) rememberNZBRefusal(id string, err error) {
	c.nzbFlightMu.Lock()
	defer c.nzbFlightMu.Unlock()
	if today := c.today(); c.nzbRefusalDay != today {
		c.nzbRefusalDay = today
		c.nzbRefusals = make(map[string]error)
	}
	c.nzbRefusals[id] = err
}

const pluginTorrentSeal = "plugin-indexer-torrent/v1"

// PluginTorrentReference replaces a torrent descriptor URL in plugin results.
func (m *Manager) PluginTorrentReference(r core.Release) string {
	if m.opts.PluginReferences == nil || r.IndexerID == "" || r.NZBURL == "" {
		return ""
	}
	data, err := json.Marshal(pluginNZBReference{ID: r.ID, IndexerID: r.IndexerID,
		GUID: r.GUID, Link: m.StripLinkAuth(r).NZBURL, Name: r.Name, Size: r.Size})
	if err != nil {
		return ""
	}
	ref, _ := m.opts.PluginReferences.Seal(pluginTorrentSeal, data)
	return ref
}

// ResolvePluginTorrent only fetches descriptor URLs issued by Ether itself.
func (m *Manager) ResolvePluginTorrent(ctx context.Context, rel core.Release) (core.Release, error) {
	if m.opts.PluginReferences == nil || len(rel.TorrentRef) == 0 || len(rel.TorrentRef) > 32768 {
		return rel, core.ErrReleaseRejected
	}
	payload, err := m.opts.PluginReferences.Unseal(pluginTorrentSeal, rel.TorrentRef)
	var stored pluginNZBReference
	if err != nil || json.Unmarshal(payload, &stored) != nil {
		return rel, core.ErrReleaseRejected
	}
	rel.ID, rel.GUID, rel.IndexerID = stored.ID, stored.GUID, stored.IndexerID
	rel.NZBURL, rel.Name, rel.Size = stored.Link, stored.Name, stored.Size
	result, err := m.ResolveTorrent(ctx, rel)
	if err != nil {
		if errors.Is(err, core.ErrBudgetExhausted) {
			return rel, core.ErrBudgetExhausted
		}
		return rel, core.ErrReleaseRejected
	}
	result.NZBURL = ""
	return result, nil
}
