package mediabrowser

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
	"github.com/braedonsaunders/ether/internal/frontend/kit/ids"
)

type embyFacetAllocator struct{}

func (embyFacetAllocator) Compact(_ context.Context, surface, kind, ref string) (int32, error) {
	if surface == "emby" && kind == "facet-named" && ref == "genre:genre-id" {
		return 7, nil
	}
	if surface == "emby" && kind == "facet-named" && ref == "studio:studio-id" {
		return 9, nil
	}
	return 0, errors.New("unknown")
}
func (embyFacetAllocator) Resolve(_ context.Context, surface, kind string, id int32) (string, error) {
	if surface == "emby" && kind == "facet-named" && id == 7 {
		return "genre:genre-id", nil
	}
	if surface == "emby" && kind == "facet-named" && id == 9 {
		return "studio:studio-id", nil
	}
	return "", errors.New("unknown")
}

func TestEmbyNumericFacetFiltersRemainViewerAuthorized(t *testing.T) {
	viewer := &frontend.Viewer{ID: 1}
	calls := 0
	experience := frontendtest.Experience{ResolveFacetFunc: func(_ context.Context, v *frontend.Viewer, _ frontend.Device, _ string, kind, id string) (core.FacetValue, error) {
		calls++
		if v != viewer || kind != "genre" || id != "genre-id" {
			return core.FacetValue{}, errors.New("denied")
		}
		return core.FacetValue{Key: "Drama"}, nil
	}}
	i := &instance{protocol: "emby", host: frontend.Host{Experience: experience, IDs: ids.Codecs{Allocator: embyFacetAllocator{}}}}
	q := frontend.ItemQuery{}
	if err := i.resolveFilters(httptest.NewRequest("GET", "/Items?GenreIds=7", nil), viewer, &q); err != nil || calls != 1 || len(q.Filter.Genres) != 1 || q.Filter.Genres[0] != "Drama" {
		t.Fatalf("query %#v, calls %d, error %v", q, calls, err)
	}
	if err := i.resolveFilters(httptest.NewRequest("GET", "/Items?GenreIds=8", nil), viewer, &q); err == nil || calls != 1 {
		t.Fatalf("unknown ID accepted, calls %d, error %v", calls, err)
	}
	other := &frontend.Viewer{ID: 2}
	if err := i.resolveFilters(httptest.NewRequest("GET", "/Items?GenreIds=7", nil), other, &q); err == nil || calls != 2 {
		t.Fatalf("viewer authorization bypassed, calls %d, error %v", calls, err)
	}
}

func TestEmbyNamedFacetNumbersNameOneKind(t *testing.T) {
	i := &instance{protocol: "emby", host: frontend.Host{IDs: ids.Codecs{Allocator: embyFacetAllocator{}}}}
	ctx := context.Background()
	if n, ok := i.namedFacetWireID(ctx, "studio", "studio-id"); !ok || n != int32(9) {
		t.Fatalf("studio wire id = %v, %v", n, ok)
	}
	if id, err := i.resolveNamedFacetWireID(ctx, "studio", "9"); err != nil || id != "studio-id" {
		t.Fatalf("studio 9 = %q, %v", id, err)
	}
	// A genre's number is not also a studio, and the reverse.
	if _, err := i.resolveNamedFacetWireID(ctx, "studio", "7"); err == nil {
		t.Fatal("genre number resolved as a studio")
	}
	if _, err := i.resolveNamedFacetWireID(ctx, "genre", "9"); err == nil {
		t.Fatal("studio number resolved as a genre")
	}
}
