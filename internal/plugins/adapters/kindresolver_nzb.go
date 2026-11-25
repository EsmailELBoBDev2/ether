package adapters

import (
	"context"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
)

// PluginNZBStore is Ether's shared indexer and vault path.
type PluginNZBStore interface {
	PluginNZBReference(context.Context, core.Release) string
	RequestPluginNZB(context.Context, string, core.Release) ([]byte, string, error)
	PluginTorrentReference(context.Context, core.Release) string
	ResolvePluginTorrent(context.Context, core.Release) (core.Release, error)
}

func (r *KindResolver) fetchPluginNZB(ctx context.Context, rel core.Release) ([]byte, error) {
	if r.pluginNZBs == nil {
		return r.indexer.FetchNZB(ctx, rel)
	}
	data, _, err := r.pluginNZBs.RequestPluginNZB(ctx, rel.NZBRef, rel)
	return data, err
}

// publicReleaseLinks mirrors opaque references into the deprecated link fields.
func (r *KindResolver) publicReleaseLinks(c core.SourceCandidate) core.SourceCandidate {
	if r.pluginNZBs == nil {
		return c
	}
	c.NZBRef = candidateNZBRef(c)
	if c.TorrentRef == "" {
		if ref, ok := strings.CutPrefix(c.TorrentURL, core.PluginTorrentRefPrefix); ok {
			c.TorrentRef = ref
		}
	}
	c.NZBURL, c.TorrentURL = "", ""
	if c.NZBRef != "" {
		c.NZBURL = core.PluginNZBRefPrefix + c.NZBRef
	}
	if c.TorrentRef != "" {
		c.TorrentURL = core.PluginTorrentRefPrefix + c.TorrentRef
	}
	return c
}

func candidateNZBRef(c core.SourceCandidate) string {
	if c.NZBRef != "" {
		return strings.TrimPrefix(c.NZBRef, core.PluginNZBRefPrefix)
	}
	locator := strings.TrimPrefix(c.NZBURL, "nzb:")
	if ref, ok := strings.CutPrefix(locator, core.PluginNZBRefPrefix); ok {
		return ref
	}
	return ""
}

func candidateNZBKey(c core.SourceCandidate) string {
	if ref := candidateNZBRef(c); ref != "" {
		return "ref:" + hashNZBBytes([]byte(ref))
	}
	return normalizeNZBKey(c.NZBURL)
}

func (r *KindResolver) requestPluginNZB(ctx context.Context, q core.SourceQuery) ([]core.SourceCandidate, error) {
	if r.pluginNZBs == nil {
		return nil, core.ErrNotAvailable
	}
	var rel core.Release
	if q.NZBIdentity != nil {
		rel.IndexerID = strings.TrimSpace(q.NZBIdentity.IndexerID)
		rel.GUID = strings.TrimSpace(q.NZBIdentity.GUID)
	}
	if len(rel.IndexerID) > 256 || len(rel.GUID) > 4096 {
		return nil, core.ErrReleaseRejected
	}
	data, storeRef, err := r.pluginNZBs.RequestPluginNZB(ctx, strings.TrimSpace(q.NZBRef), rel)
	if err != nil {
		return nil, err
	}
	doc, err := nzb.Parse(data)
	if err != nil {
		return nil, core.ErrReleaseRejected
	}
	files := make([]core.ReleaseFile, 0, min(len(doc.Files), 2000))
	for i, file := range doc.Files[:cap(files)] {
		files = append(files, core.ReleaseFile{Index: i, Path: file.StableName(), Size: file.EstimatedDecodedBytes(), SizeApproximate: true})
	}
	return []core.SourceCandidate{r.publicReleaseLinks(core.SourceCandidate{NZBRef: storeRef, Files: files, FileCount: len(doc.Files)})}, nil
}
