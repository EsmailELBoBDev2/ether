package livetv

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

const maxAppBatchChannels = 200_000

// ChannelAppChange sets one app's visibility across an explicit channel list,
// a source (optionally narrowed to one provider group), or an owner group.
type ChannelAppChange struct {
	App        string   `json:"app"`
	Show       *bool    `json:"show"`
	Channels   []string `json:"channels,omitempty"`
	Source     *string  `json:"source,omitempty"`
	Group      *string  `json:"group,omitempty"`
	OwnerGroup string   `json:"ownerGroup,omitempty"`
}

func (s *Service) SetChannelsApp(ctx context.Context, change ChannelAppChange) (int, error) {
	if !slices.Contains(Apps, change.App) {
		return 0, fmt.Errorf("unknown Live TV app %q", change.App)
	}
	if change.Show == nil {
		return 0, fmt.Errorf("show is required")
	}
	if change.Group != nil && change.Source == nil {
		return 0, fmt.Errorf("source is required with group")
	}
	scopes := 0
	if len(change.Channels) > 0 {
		scopes++
	}
	if change.Source != nil {
		scopes++
	}
	if change.OwnerGroup != "" {
		scopes++
	}
	if scopes != 1 {
		return 0, fmt.Errorf("choose one of channels, source, or ownerGroup")
	}
	if len(change.Channels) > maxAppBatchChannels {
		return 0, fmt.Errorf("select at most %d channels", maxAppBatchChannels)
	}

	var explicitIDs []string
	if len(change.Channels) > 0 {
		explicitIDs = slices.Clone(change.Channels)
		slices.Sort(explicitIDs)
		explicitIDs = slices.Compact(explicitIDs)
		if explicitIDs[0] == "" {
			return 0, fmt.Errorf("channel id is required")
		}
	}

	var ownerGroupIDs []string
	if change.OwnerGroup != "" {
		ids, handled, count, err := s.applyOwnerGroupAppRule(ctx, change.OwnerGroup, change.App, *change.Show)
		if err != nil {
			return 0, err
		}
		if handled {
			return count, nil
		}
		ownerGroupIDs = ids
	}

	d := store.DialectOf(s.o.DB)
	tx, err := s.o.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var ids []string
	switch {
	case len(explicitIDs) > 0:
		ids = explicitIDs
		if err := s.lockChannelBatchSources(ctx, tx, ids); err != nil {
			return 0, err
		}
	case change.Source != nil:
		if *change.Source != "" && *change.Source != "virtual" {
			if err := s.lockSourceEdits(ctx, tx, []string{*change.Source}); err != nil {
				return 0, err
			}
		}
		ids, err = selectSourceGroupChannelIDs(ctx, d, tx, *change.Source, change.Group)
		if err != nil {
			return 0, err
		}
	case change.OwnerGroup != "":
		ids = ownerGroupIDs
		if len(ids) > 0 {
			if err := s.lockChannelBatchSources(ctx, tx, ids); err != nil {
				return 0, err
			}
		}
	}

	if len(ids) > maxAppBatchChannels {
		return 0, fmt.Errorf("target exceeds %d channels", maxAppBatchChannels)
	}

	if len(ids) > 0 {
		current, err := channelHiddenApps(ctx, d, tx, ids)
		if err != nil {
			return 0, err
		}
		if len(explicitIDs) > 0 && len(current) != len(ids) {
			return 0, core.ErrNotFound
		}
		groups := map[string][]string{}
		for _, id := range ids {
			cur, ok := current[id]
			if !ok {
				continue
			}
			var next []string
			if *change.Show {
				next = slices.DeleteFunc(slices.Clone(cur), func(app string) bool { return app == change.App })
			} else {
				next, _ = normalizeHiddenApps(append(slices.Clone(cur), change.App))
			}
			if slices.Equal(cur, next) {
				continue
			}
			payload, err := json.Marshal(map[string]any{"hiddenApps": next})
			if err != nil {
				return 0, err
			}
			groups[string(payload)] = append(groups[string(payload)], id)
		}
		expression := "json_patch(data,?)"
		if d == store.Postgres {
			expression = "(data::jsonb || ?::jsonb)::text"
		}
		for payload, batch := range groups {
			if err := patchChannels(ctx, tx, expression, payload, batch); err != nil {
				return 0, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.bumpLineup()
	return len(ids), nil
}

func selectSourceGroupChannelIDs(ctx context.Context, d store.Dialect, tx *sql.Tx, source string, group *string) ([]string, error) {
	var where string
	var args []any
	if source == "virtual" {
		where = store.JSONText(d, "data", "kind") + " = ?"
		args = append(args, "virtual")
	} else {
		where = "COALESCE(" + store.JSONText(d, "data", "sourceId") + ", '') = ?"
		args = append(args, source)
	}
	if group != nil {
		where += " AND COALESCE(" + store.JSONText(d, "data", "group") + ", '') = ?"
		args = append(args, *group)
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM livetv_channels WHERE "+where+" ORDER BY id LIMIT ?", append(args, maxAppBatchChannels+1)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// applyOwnerGroupAppRule resolves an owner group's member channels for a bulk
// app toggle. When group-level app rules are present, routing the group rule
// update here keeps SetChannelsApp's contract unchanged.
func (s *Service) applyOwnerGroupAppRule(ctx context.Context, groupID, _ string, _ bool) (ids []string, handled bool, updated int, err error) {
	guides, err := s.OwnerGuides(ctx)
	if err != nil {
		return nil, false, 0, err
	}
	var chosen *OwnerGuide
	for i := range guides {
		if guides[i].ID == groupID {
			chosen = &guides[i]
			break
		}
	}
	if chosen == nil {
		return nil, false, 0, core.ErrNotFound
	}
	summaries, err := s.ChannelSummaries(ctx)
	if err != nil {
		return nil, false, 0, err
	}
	groupSet := make(map[GuideGroup]bool, len(chosen.Groups))
	for _, g := range chosen.Groups {
		groupSet[g] = true
	}
	explicit := make(map[string]bool, len(chosen.Channels))
	for _, id := range chosen.Channels {
		explicit[id] = true
	}
	for _, c := range summaries {
		if chosen.All || groupSet[GuideGroup{SourceID: c.SourceID, Name: c.Group}] || explicit[c.ID] {
			ids = append(ids, c.ID)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	return ids, false, 0, nil
}
