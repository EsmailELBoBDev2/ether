package mediabrowser

import (
	"context"
	"strconv"
	"strings"
)

// embyNamedFacetKind is the one compact-id namespace genres and studios
// share on Emby. Emby Web opens a NameLongIdPair's number as an item id
// (Users/{u}/Items/{n}), so a number must name a single genre or studio.
const embyNamedFacetKind = "facet-named"

// Emby's NameLongIdPair uses a numeric ID; Jellyfin uses the Host's GUID.
func (i *instance) namedFacetWireID(ctx context.Context, kind, id string) (any, bool) {
	if i.protocol != "emby" {
		return id, true
	}
	n, err := i.host.IDs.Compact(ctx, "emby", embyNamedFacetKind, kind+":"+id)
	return n, err == nil && n > 0
}

func (i *instance) resolveNamedFacetWireID(ctx context.Context, kind, id string) (string, error) {
	if i.protocol != "emby" || (kind != "genre" && kind != "studio") {
		return id, nil
	}
	n, err := strconv.ParseInt(id, 10, 32)
	if err != nil {
		// Preserve ordinary GUID-based filters from the facet list routes.
		return id, nil
	}
	if n <= 0 {
		return "", strconv.ErrSyntax
	}
	ref, err := i.host.IDs.Resolve(ctx, "emby", embyNamedFacetKind, int32(n))
	if err != nil {
		return "", err
	}
	facetID, ok := strings.CutPrefix(ref, kind+":")
	if !ok || facetID == "" {
		return "", strconv.ErrSyntax
	}
	return facetID, nil
}
