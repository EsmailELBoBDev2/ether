package mediabrowser

import (
	"net/http"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/kit/ids"
)

// Ether does not support locking individual view metadata fields.
// Empty image and trailer collections are the absence of those assets, not
// invented artwork.
func (i *instance) embyViewDTO(view map[string]any) {
	view["LockData"] = false
	view["LockedFields"] = []string{}
	view["BackdropImageTags"] = []any{}
	view["ImageTags"] = map[string]any{}
	view["ExternalUrls"] = []any{}
	view["RemoteTrailers"] = []any{}
	view["Taglines"] = []any{}
	view["UserData"] = map[string]any{"IsFavorite": false, "PlaybackPositionTicks": 0, "Played": false}
	if name, ok := view["Name"].(string); ok && view["ForcedSortName"] == nil {
		view["ForcedSortName"] = name
	}
	if kind, ok := view["CollectionType"].(string); ok {
		view["Subviews"] = embySubviews(kind)
	}
	id, _ := view["Id"].(string)
	if id != "" {
		view["Guid"] = id
		view["PresentationUniqueKey"] = id
		view["Etag"] = embyETag(id)
	}
}

// embySubviews lists the tabs an Emby 4.10 library offers, which Emby Web
// reads from the library item (Subviews) to build its tab bar and refuses to
// render without. Only tabs Ether can fill are listed.
func embySubviews(collectionType string) []string {
	switch collectionType {
	case "movies":
		return []string{"movies", "collections", "genres"}
	case "tvshows":
		return []string{"series", "episodes", "genres"}
	case "music":
		return []string{"albums", "artists", "genres"}
	case "books":
		return []string{"books", "genres"}
	case "audiobooks":
		// Emby Web opens audiobooks in its music view: "albums" is its Audio
		// Books tab and "artists" its Authors tab.
		return []string{"albums", "artists", "genres"}
	case "homevideos", "photos", "musicvideos", "mixed":
		return []string{"videos"}
	default:
		return []string{}
	}
}

// embyAudiobookAlbums: Emby Web opens an audiobooks library in its music view,
// whose first tab lists IncludeItemTypes=MusicAlbum ("Audio Books"). Ether's
// audiobooks are AudioBook items, so inside an audiobooks library that type
// means the audiobooks themselves.
func (i *instance) embyAudiobookAlbums(r *http.Request, v *frontend.Viewer, viewNum int64, q frontend.ItemQuery) frontend.ItemQuery {
	if i.protocol != "emby" || len(q.UnservedTypes) == 0 || i.host.Experience == nil {
		return q
	}
	asked := false
	for _, typ := range q.UnservedTypes {
		if typ == "musicalbum" {
			asked = true
		}
	}
	if !asked {
		return q
	}
	views, err := i.host.Experience.Views(r.Context(), v)
	if err != nil {
		return q
	}
	for _, view := range views {
		if view.ID == ids.Decimal(viewNum) && mediaBrowserCollectionType(view) == "audiobooks" {
			out := q
			out.UnservedTypes = make([]string, 0, len(q.UnservedTypes))
			for _, typ := range q.UnservedTypes {
				if typ == "musicalbum" {
					typ = "audiobook"
				}
				out.UnservedTypes = append(out.UnservedTypes, typ)
			}
			return out
		}
	}
	return q
}
