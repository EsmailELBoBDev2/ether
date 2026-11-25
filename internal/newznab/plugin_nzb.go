package newznab

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/vault"
)

const pluginNZBSeal = "plugin-indexer-release/v1"

var errPluginNZB = fmt.Errorf("indexer NZB unavailable: %w", core.ErrSourceUnavailable)

// IsIndexerHost is evaluated against the current configuration on every call.
func (m *Manager) IsIndexerHost(host string) bool {
	for _, c := range m.Clients() {
		cfg := c.Config()
		if cfg.IsAddon() || cfg.IsStremio() {
			continue
		}
		u, err := url.Parse(cfg.URL)
		if err == nil && strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), strings.TrimSuffix(host, ".")) {
			return true
		}
	}
	return false
}

type pluginNZBContextKey struct{}

func isPluginNZB(ctx context.Context) bool {
	return ctx.Value(pluginNZBContextKey{}) != nil
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

const pluginRefVersion = "r1:"

func (m *Manager) savePluginRef(ctx context.Context, purpose string, release pluginNZBReference) string {
	if m.opts.PluginReferences == nil || (release.Key == "" && (release.IndexerID == "" || release.ID == "")) {
		return ""
	}
	// Hash the release identity, independent of mutable result metadata and
	// download links. Only an issued reference can resolve to a descriptor.
	ref := fmt.Sprintf("%s%x", pluginRefVersion, sha256.Sum256([]byte(
		purpose+"\x00"+string(release.Key)+"\x00"+release.IndexerID+"\x00"+release.ID)))
	data, err := json.Marshal(release)
	if err != nil {
		return ""
	}
	sealed, err := m.opts.PluginReferences.Seal(purpose+"/"+ref, data)
	if err != nil {
		return ""
	}
	if m.db == nil {
		m.pluginRefs.Store(ref, sealed)
	} else if _, err := m.db.ExecContext(ctx, `INSERT INTO newznab_plugin_refs(ref,descriptor)
		VALUES (?,?) ON CONFLICT(ref) DO UPDATE SET descriptor=excluded.descriptor`, ref, sealed); err != nil {
		return ""
	}
	return ref
}

func (m *Manager) openPluginRef(ctx context.Context, purpose, ref string) (pluginNZBReference, error) {
	var release pluginNZBReference
	if m.opts.PluginReferences == nil || ref == "" || len(ref) > 32768 {
		return release, errPluginNZB
	}
	sealed := ref
	if strings.HasPrefix(ref, pluginRefVersion) {
		if len(ref) != len(pluginRefVersion)+sha256.Size*2 {
			return release, errPluginNZB
		}
		if m.db == nil {
			value, ok := m.pluginRefs.Load(ref)
			if !ok {
				return release, errPluginNZB
			}
			sealed = value.(string)
		} else if err := m.db.QueryRowContext(ctx, `SELECT descriptor FROM newznab_plugin_refs WHERE ref=?`, ref).Scan(&sealed); err != nil {
			return release, errPluginNZB
		}
		purpose += "/" + ref
	}
	data, err := m.opts.PluginReferences.Unseal(purpose, sealed)
	if err != nil || json.Unmarshal(data, &release) != nil {
		return pluginNZBReference{}, errPluginNZB
	}
	return release, nil
}

// PluginNZBReference issues a portable, authenticated reference from a real
// search result. Credential-bearing links stay in the server-side descriptor.
func (m *Manager) PluginNZBReference(ctx context.Context, r core.Release) string {
	if (r.Protocol != "" && r.Protocol != core.ProtocolUsenet) || r.InfoHash != "" || r.MagnetURL != "" {
		return ""
	}
	key := vault.KeyOf(r)
	if key == "" && (r.IndexerID == "" || (r.NZBURL == "" && r.GUID == "")) {
		return ""
	}
	if key != "" {
		return m.savePluginRef(ctx, pluginNZBSeal, pluginNZBReference{Key: key})
	}
	return m.savePluginRef(ctx, pluginNZBSeal, pluginNZBReference{
		ID: r.ID, GUID: r.GUID, IndexerID: r.IndexerID,
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
	if ref == "" {
		locator := strings.TrimPrefix(strings.TrimSpace(hint.NZBURL), "nzb:")
		if value, ok := strings.CutPrefix(locator, core.PluginNZBRefPrefix); ok {
			ref = value
		}
	}
	ref = strings.TrimPrefix(ref, core.PluginNZBRefPrefix)
	var key vault.Key
	var data []byte
	var storedAlready bool
	var nativeIdentity bool
	var err error
	switch {
	case ref != "":
		stored, openErr := m.openPluginRef(ctx, pluginNZBSeal, ref)
		if openErr != nil {
			return nil, "", openErr
		}
		key = stored.Key
		storedAlready = key != ""
		hint = core.Release{ID: stored.ID, GUID: stored.GUID, IndexerID: stored.IndexerID,
			NZBURL: stored.Link, Name: stored.Name, Size: stored.Size, Category: stored.Category, ContentID: stored.ContentID}
	case hint.NZBURL != "":
		hint, err = m.legacyPluginNZB(hint)
	case hint.IndexerID != "" && hint.GUID != "":
		nativeIdentity = true
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
	if key == "" && err == nil && nativeIdentity {
		var client *Client
		client, err = m.releaseClient(hint)
		if err == nil && (client.Config().UsesBackend() || client.Config().IsZilean()) {
			err = errPluginNZB
		}
	}
	if err == nil && key == "" && hint.ID == "" {
		return nil, "", errPluginNZB
	}
	if err == nil && key != "" {
		data, err = m.FetchNZB(ctx, core.Release{IndexerID: vault.IndexerID, GUID: "vault:" + string(key)})
	} else if err == nil {
		data, err = m.FetchNZB(context.WithValue(ctx, pluginNZBContextKey{}, true), hint)
	}
	if err != nil {
		var refusal *GrabRefusal
		if errors.As(err, &refusal) {
			return nil, "", refusal
		}
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
		storeRef = m.savePluginRef(ctx, pluginNZBSeal, pluginNZBReference{Key: key})
	}
	if storeRef == "" {
		return nil, "", errPluginNZB
	}
	return data, storeRef, nil
}

func (m *Manager) legacyPluginNZB(hint core.Release) (core.Release, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(hint.NZBURL), "nzb:")
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Host == "" {
		return core.Release{}, errPluginNZB
	}
	q := u.Query()
	var selected core.Release
	for _, c := range m.Clients() {
		cfg := c.Config()
		if cfg.UsesBackend() || cfg.IsZilean() || !sameAuthOrigin(cfg.URL, u.String()) {
			continue
		}
		if hint.IndexerID != "" && hint.IndexerID != c.indexerID() {
			continue
		}
		base, err := url.Parse(cfg.URL)
		if err != nil {
			continue
		}
		basePath := strings.TrimRight(base.Path, "/")
		root := strings.TrimSuffix(strings.TrimSuffix(basePath, "/api/newznab"), "/api")
		var guid string
		downloadParams := url.Values{}
		switch {
		case (u.Path == endpointPathFor(basePath, false) || u.Path == endpointPathFor(basePath, true)) && strings.EqualFold(q.Get("t"), "get"):
			guid = q.Get("id")
			downloadParams.Set("t", "get")
			downloadParams.Set("id", guid)
		case u.Path == root+"/download":
			guid = q.Get("guid")
			if guid != "" {
				downloadParams.Set("guid", guid)
			} else if strings.HasPrefix(cfg.ID, "prowlarr-") {
				if _, err := strconv.Atoi(path.Base(root)); err == nil {
					guid = q.Get("link")
					downloadParams.Set("link", guid)
					if file := q.Get("file"); file != "" {
						downloadParams.Set("file", file)
					}
				}
			}
		case path.Dir(u.Path) == root+"/getnzb", path.Dir(u.Path) == root+"/nzb", path.Dir(u.Path) == root+"/download":
			guid = strings.TrimSuffix(path.Base(u.Path), ".nzb")
		}
		download := *u
		download.RawPath = ""
		download.RawQuery = downloadParams.Encode()
		link := download.String()
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

func nzbReleaseIDs(r core.Release) []string {
	ids := []string{r.ID}
	if pack := nzbFlightKey(r); pack != r.ID {
		ids = append(ids, pack)
	}
	return ids
}

func (c *Client) nzbIdentities(r core.Release) []string {
	ids := nzbReleaseIDs(r)
	if alias := c.nzbLinkAlias(r.NZBURL); alias != "" {
		ids = append(ids, alias)
	}
	if r.GUID != "" {
		ids = append(ids, releaseID(c.indexerID(), r.GUID, ""))
	}
	return ids
}

func (c *Client) nzbRefusal(ctx context.Context, r core.Release) error {
	ids := c.nzbIdentities(r)
	now := c.now()
	if c.db != nil {
		var params [4]string
		copy(params[:], ids)
		var until int64
		err := c.db.QueryRowContext(ctx, `SELECT COALESCE(max(retry_at),0)
			FROM newznab_nzb_refusals WHERE indexer_id=? AND release_id IN (?,?,?,?)`,
			c.indexerID(), params[0], params[1], params[2], params[3]).Scan(&until)
		if err != nil {
			return err
		}
		if now.Before(time.Unix(until, 0)) {
			return &GrabRefusal{RetryAt: time.Unix(until, 0)}
		}
	}
	c.nzbFlightMu.Lock()
	defer c.nzbFlightMu.Unlock()
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, blocked := c.nzbStoreFailures[id]; blocked {
			return vault.ErrPayloadMissing
		}
		if until := c.nzbRefusals[id]; now.Before(until) {
			return &GrabRefusal{RetryAt: until}
		}
	}
	return nil
}

func (c *Client) rememberNZBRefusal(ctx context.Context, r core.Release, until time.Time) {
	ids := c.nzbIdentities(r)
	now := c.now()
	if c.db != nil {
		_, _ = c.db.ExecContext(ctx, `DELETE FROM newznab_nzb_refusals WHERE retry_at<=?`, now.Unix())
		for _, id := range ids {
			if id != "" {
				_, _ = c.db.ExecContext(ctx, `INSERT INTO newznab_nzb_refusals(indexer_id, release_id, retry_at)
					VALUES (?,?,?) ON CONFLICT(indexer_id,release_id) DO UPDATE SET retry_at=excluded.retry_at
					WHERE excluded.retry_at>newznab_nzb_refusals.retry_at`, c.indexerID(), id, until.Unix())
			}
		}
	}
	c.nzbFlightMu.Lock()
	defer c.nzbFlightMu.Unlock()
	if c.nzbRefusals == nil {
		c.nzbRefusals = make(map[string]time.Time)
	}
	if len(c.nzbRefusals) >= 4096 {
		maps.DeleteFunc(c.nzbRefusals, func(_ string, deadline time.Time) bool { return !now.Before(deadline) })
	}
	for _, id := range ids {
		if id != "" && until.After(c.nzbRefusals[id]) {
			c.nzbRefusals[id] = until
		}
	}
}

// Leave most of the PostgreSQL pool available for vault writes and budget
// accounting while a session connection is reserved during the network call.
var nzbSessionSlots = make(chan struct{}, 4)

func (c *Client) lockNZBGrab(ctx context.Context, id string) (func(), error) {
	if c.db == nil || c.vault == nil || store.DialectOf(c.db) != store.Postgres {
		return nil, nil
	}
	select {
	case nzbSessionSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	unlock, err := store.SessionLock(ctx, c.db, "nzb-grab:"+c.indexerID()+":"+id)
	if err != nil {
		<-nzbSessionSlots
		return nil, err
	}
	return func() {
		unlock()
		<-nzbSessionSlots
	}, nil
}

const pluginTorrentSeal = "plugin-indexer-torrent/v1"

// PluginTorrentReference replaces a torrent descriptor URL in plugin results.
func (m *Manager) PluginTorrentReference(ctx context.Context, r core.Release) string {
	if r.IndexerID == "" || r.NZBURL == "" {
		return ""
	}
	return m.savePluginRef(ctx, pluginTorrentSeal, pluginNZBReference{ID: r.ID, IndexerID: r.IndexerID,
		GUID: r.GUID, Link: m.StripLinkAuth(r).NZBURL, Name: r.Name, Size: r.Size})
}

// ResolvePluginTorrent only fetches descriptor URLs issued by Ether itself.
func (m *Manager) ResolvePluginTorrent(ctx context.Context, rel core.Release) (core.Release, error) {
	ref := rel.TorrentRef
	if ref == "" {
		var ok bool
		ref, ok = strings.CutPrefix(rel.NZBURL, core.PluginTorrentRefPrefix)
		if !ok {
			return rel, core.ErrReleaseRejected
		}
	}
	stored, err := m.openPluginRef(ctx, pluginTorrentSeal, strings.TrimPrefix(ref, core.PluginTorrentRefPrefix))
	if err != nil {
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
