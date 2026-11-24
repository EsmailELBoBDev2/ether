package collections

import (
	"context"
	"fmt"

	"github.com/braedonsaunders/ether/internal/core"
)

// MaxCatalogueSourceItems bounds how many items one catalogue-source sync run
// gathers and matches per list so huge external lists page safely without
// starving sibling lists or timing out the sync batch.
const MaxCatalogueSourceItems = 250

// GatherCatalogueSource reuses the installed list-source adapter, including its
// paging limits and external-ID matching. Only an opened library invokes it.
// kind is the list-source kind id, label names the list for the owner, and
// mediaType is "movie" or "show".
func (s *Service) GatherCatalogueSource(ctx context.Context, kind, label, mediaType string) ([]core.Title, error) {
	k, ok := s.kindRegistry().Lookup(kind)
	if !ok {
		return nil, fmt.Errorf("%s isn't loaded on this server yet; turn its plugin off and on again in Plugins, or restart Ether", label)
	}
	gatherKind := "movie"
	if mediaType == string(core.TypeShow) {
		gatherKind = "tv"
	}
	src := core.CollectionSource{Kind: kind, MaxItems: MaxCatalogueSourceItems}
	g := &Gather{Strict: true, Ctx: ctx, Svc: s, Spec: &core.CollectionSpec{}, Src: src, Want: MaxCatalogueSourceItems, Kinds: []string{gatherKind}}
	k.Gather(g)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return g.Titles(), g.Res.err
}
