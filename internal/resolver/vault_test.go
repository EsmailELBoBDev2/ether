package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
	"github.com/braedonsaunders/ether/internal/vault"
)

type replayVault struct {
	vault.Store
	entry vault.Entry
}

func (v *replayVault) Search(context.Context, core.SearchRequest) ([]vault.Entry, error) {
	return []vault.Entry{v.entry}, nil
}
func (v *replayVault) Entry(context.Context, vault.Key) (vault.Entry, error) { return v.entry, nil }
func (v *replayVault) Get(context.Context, vault.Key) (*vault.Record, error) {
	return &vault.Record{NZB: &nzb.NZB{Files: []*nzb.File{{Segments: []nzb.Segment{{MessageID: "sample"}}}}}}, nil
}
func (v *replayVault) MarkHealth(_ context.Context, _ vault.Key, h vault.Health) error {
	v.entry.Health = h
	return nil
}
func (v *replayVault) VerifyArticles(context.Context, []string, []string) (bool, error) {
	return false, nil
}
func (v *replayVault) Touch(context.Context, vault.Key, bool) error { return nil }
func (v *replayVault) ByAlias(context.Context, string, string) (vault.Key, bool, error) {
	return "", false, nil
}

func TestLibraryReplayCutoffFingerprintAndDeadFallback(t *testing.T) {
	for _, scenario := range []string{"replay", "lower quality", "changed profile", "dead"} {
		t.Run(scenario, func(t *testing.T) {
			req := core.ResolveRequest{Title: matrixTitle(), Client: webCaps()}
			p := core.QualityProfile{Name: "Library", MaxHeight: 1080, Language: "any"}
			v := &replayVault{entry: vault.Entry{Key: "sample", Kind: vault.KindUsenet, Name: "The.Signal.1999.1080p.WEB-DL.H264-GRP", Size: 8000 << 20, Health: vault.HealthOK, HealthAt: time.Now()}}
			idx := &fakeIndexer{releases: []core.Release{rel("live", "The.Signal.1999.1080p.BluRay.x264.Proper-GRP", 8100)}}
			s := New(idx, fakeHealth{}, nil, testCfg(), Options{Vault: v, Verifier: v})
			v.entry.ProfileFP = ProfileFingerprint(p, wantedAudio(p, req.User, s.catalogLanguage()))
			switch scenario {
			case "changed profile":
				v.entry.ProfileFP = "older"
			case "lower quality":
				v.entry.Name = "The.Signal.1999.720p.WEB-DL.H264-GRP"
			case "dead":
				v.entry.HealthAt = time.Time{}
			}
			res, err := s.resolve(context.Background(), req, p)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "replay" {
				if res.Release.ID != "vault-sample" || idx.searches.Load() > 0 {
					t.Fatalf("expected library replay, got %s", res.Release.ID)
				}
			} else if res.Release.ID != "live" {
				t.Fatalf("expected live result, got %s", res.Release.ID)
			}
			if scenario == "dead" && v.entry.Health != vault.HealthDead {
				t.Fatal("dead sample was not recorded")
			}
		})
	}
}
