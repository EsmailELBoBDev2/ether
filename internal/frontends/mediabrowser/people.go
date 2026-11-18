package mediabrowser

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/frontend"
)

// personSearchDTO builds a Person BaseItemDto for /Persons and person detail
// routes, attaching a Primary image grant when a portrait is available.
func (i *instance) personSearchDTO(r *http.Request, token string, person frontend.PersonDetail) map[string]any {
	dto := map[string]any{
		"Id":       person.ID,
		"Name":     person.Name,
		"Type":     "Person",
		"ServerId": i.info.ID,
	}
	if person.ArtworkItem != 0 && person.TMDB > 0 && i.host.ImageGrants != nil && token != "" {
		subject := "tmdb:" + strconv.FormatInt(person.TMDB, 10)
		for _, img := range person.Images {
			if img.Kind != frontend.ImagePerson || img.Subject != subject || img.Path == "" {
				continue
			}
			grant, err := i.host.ImageGrants.Issue(r.Context(), frontend.ImageGrantRequest{
				Credential: token,
				Device:     device(r),
				Item:       person.ArtworkItem,
				Image:      img,
			})
			if err != nil || grant == "" {
				continue
			}
			dto["ImageTags"] = map[string]string{"Primary": grant}
			i.addImageMetadata(dto, "Primary", grant, img)
			delete(dto, "PrimaryImageAspectRatio")
			break
		}
	}
	return dto
}

// personSearchHints builds SearchHint entries for matched people when a client
// requests /Search/Hints?IncludePeople=true without an item-type restriction.
func (i *instance) personSearchHints(r *http.Request, viewer *frontend.Viewer, token, term string, q frontend.ItemQuery) []any {
	if !queryBool(r, "IncludePeople", false) || len(q.Types) > 0 || len(q.UnservedTypes) > 0 || q.Offset > 0 {
		return nil
	}
	reader, ok := i.host.Experience.(frontend.PeopleReader)
	if !ok {
		return nil
	}
	people, err := reader.People(r.Context(), viewer, device(r), frontend.PeopleQuery{
		Query: strings.TrimSpace(term),
		Limit: min(max(q.Limit, 1), 20),
	})
	if err != nil || len(people) == 0 {
		return nil
	}
	out := make([]any, 0, len(people))
	for _, person := range people {
		if person.ID == "" || person.Name == "" {
			continue
		}
		dto := i.personSearchDTO(r, token, person)
		hint := map[string]any{
			"Id":     person.ID,
			"ItemId": person.ID,
			"Name":   person.Name,
			"Type":   "Person",
		}
		if tags, ok := dto["ImageTags"].(map[string]string); ok && tags["Primary"] != "" {
			hint["PrimaryImageTag"] = tags["Primary"]
		}
		out = append(out, hint)
	}
	return out
}

// mergePersonSearchHints places person hints after direct title matches and
// ahead of people-derived title matches, bounded to room.
func mergePersonSearchHints(titles []frontend.Title, titleHints, personHints []any, room int) []any {
	if len(personHints) == 0 {
		if len(titleHints) > room {
			return titleHints[:room]
		}
		return titleHints
	}
	split := len(titleHints)
	for idx, t := range titles {
		if idx < len(titleHints) && t.SearchScore > 0 && t.SearchScore <= 0.70 {
			split = idx
			break
		}
	}
	merged := make([]any, 0, len(titleHints)+len(personHints))
	merged = append(merged, titleHints[:split]...)
	merged = append(merged, personHints...)
	merged = append(merged, titleHints[split:]...)
	if len(merged) > room {
		merged = merged[:room]
	}
	return merged
}

func normalizePersonWireID(wireID string) (string, bool) {
	clean := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(wireID), "-", ""))
	if len(clean) != 32 {
		return "", false
	}
	for i := 0; i < len(clean); i++ {
		c := clean[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return "", false
		}
	}
	return clean, true
}

// writePersonItem serves /Items/{id} and /Users/{userId}/Items/{id} when {id}
// is a person facet GUID, allowing Jellyfin/Emby clients to open performer
// detail pages from search or title credits.
func (i *instance) writePersonItem(w http.ResponseWriter, r *http.Request, viewer *frontend.Viewer, token, wireID string) bool {
	facetID, ok := normalizePersonWireID(wireID)
	if !ok || i.host.Experience == nil {
		return false
	}
	reader, ok := i.host.Experience.(frontend.PeopleReader)
	if !ok {
		return false
	}
	person, err := reader.Person(i.identityContext(r), viewer, device(r), facetID, frontend.PersonReadOptions{})
	if err != nil || person.ID != facetID || person.Name == "" {
		return false
	}
	dto := i.personSearchDTO(r, token, person)
	dto["CanDelete"] = false
	dto["CanDownload"] = false
	dto["IsFolder"] = false
	providers := map[string]string{}
	if person.TMDB > 0 && !frontend.IsPluginPersonID(person.TMDB) {
		providers["Tmdb"] = strconv.FormatInt(person.TMDB, 10)
	}
	if person.IMDB != "" {
		providers["Imdb"] = person.IMDB
	}
	dto["ProviderIds"] = providers
	if person.Biography != "" {
		dto["Overview"] = person.Biography
	}
	if person.Birthday != "" {
		dto["PremiereDate"] = person.Birthday
	}
	if tags, ok := dto["ImageTags"].(map[string]string); ok && tags["Primary"] != "" {
		dto["PrimaryImageTag"] = tags["Primary"]
	} else {
		dto["ImageTags"] = map[string]string{}
	}
	i.replyItems(w, r, dto)
	return true
}

// peopleDTO uses parent-authorized facet IDs and title-bound portrait grants.
func (i *instance) peopleDTO(r *http.Request, viewer *frontend.Viewer, token string, detail frontend.ItemDetail) []any {
	type credit struct {
		id               int64
		wire             string
		name, role, kind string
	}
	var credits []credit
	for _, p := range detail.Title.Cast {
		credits = append(credits, credit{p.TMDB, p.ID, p.Name, p.Role, "Actor"})
	}
	for _, p := range detail.Title.Directors {
		credits = append(credits, credit{p.TMDB, p.ID, p.Name, p.Role, "Director"})
	}
	for _, p := range detail.Title.Writers {
		credits = append(credits, credit{p.TMDB, p.ID, p.Name, p.Role, "Writer"})
	}
	ids := make([]int64, 0, min(len(credits), 256))
	seen := map[int64]bool{}
	for _, c := range credits {
		if c.id <= 0 || seen[c.id] {
			continue
		}
		if len(ids) == 256 {
			break
		}
		seen[c.id] = true
		ids = append(ids, c.id)
	}
	facets := map[int64]string{}
	if len(ids) > 0 {
		resolved, err := i.host.Experience.PersonFacetIDs(r.Context(), viewer, device(r), detail.Title.Key, ids)
		if err != nil {
			return nil
		}
		facets = resolved
	}
	portraits := map[string]frontend.Image{}
	if detail.ImagesAuthoritative && i.host.ImageGrants != nil {
		for _, img := range detail.Images {
			if img.Kind == frontend.ImagePerson && img.Subject != "" {
				if _, ok := portraits[img.Subject]; !ok {
					portraits[img.Subject] = img
				}
			}
		}
	}
	grants := map[int64]frontend.Image{}
	granted := map[int64]string{}
	attempted := map[int64]bool{}
	result := make([]any, 0, len(credits))
	for _, c := range credits {
		if c.name == "" {
			continue
		}
		id := ""
		if c.id > 0 {
			id = facets[c.id]
			if id == "" {
				continue
			}
		} else {
			id = c.wire
		}
		dto := map[string]any{"Name": c.name, "Type": c.kind}
		if id != "" {
			dto["Id"] = id
		}
		if c.role != "" {
			dto["Role"] = c.role
		}
		if !attempted[c.id] {
			attempted[c.id] = true
			if img, ok := portraits["tmdb:"+strconv.FormatInt(c.id, 10)]; ok {
				grant, err := i.host.ImageGrants.Issue(r.Context(), frontend.ImageGrantRequest{Credential: token, Device: device(r), Item: detail.Title.Key, Image: img})
				if err == nil {
					grants[c.id] = img
					granted[c.id] = grant
				}
			}
		}
		if grant := granted[c.id]; grant != "" {
			dto["PrimaryImageTag"] = grant
			i.addImageMetadata(dto, "Primary", grant, grants[c.id])
			// A person record carries the portrait hash, not the item aspect ratio.
			delete(dto, "PrimaryImageAspectRatio")
		}
		result = append(result, dto)
	}
	return result
}
