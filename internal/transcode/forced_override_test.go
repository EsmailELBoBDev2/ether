package transcode

import (
	"reflect"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

// Forced edits are sparse: neither untouched runtime facts nor declarations
// belonging to other identities may be replaced by the registry baseline.
func TestForcedOverridePrecedence(t *testing.T) {
	on := true
	c := core.ClientCaps{Product: "Plex for Android (TV)", Platform: "Android", Device: "Xiaomi TV Box", VideoCodecs: []string{"hevc"}, AudioCodecs: []string{"truehd"}, MaxHeight: 1080, DVProfiles: []int{5, 8}}
	o := core.ClientCapabilityOverride{Platform: "Android", Device: "Xiaomi TV Box", DolbyVision: &on, DolbyVisionProfiles: []int{5, 7, 8}}
	normal := ApplyForcedOverrides(c, "", []core.ClientCapabilityOverride{o})
	if !reflect.DeepEqual(normal, c) {
		t.Fatalf("non-forced edit changed declarations: %+v", normal)
	}
	o.Force = true
	got := ApplyForcedOverrides(c, "", []core.ClientCapabilityOverride{o})
	want := c
	want.DolbyVision = true
	want.DVProfiles = []int{5, 7, 8}
	want.ForcedOverrideApplied = true
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sparse force: got %+v, want %+v", got, want)
	}
	if !SupportsDVProfile(got, 7) {
		t.Fatal("forced profile 7 unavailable")
	}
	o.Platform = "tvOS"
	if got := ApplyForcedOverrides(c, "", []core.ClientCapabilityOverride{o}); !reflect.DeepEqual(got, c) {
		t.Fatal("edit crossed identity")
	}
	o.Platform = "Android"
	o.DolbyVisionProfiles = []int{}
	got = ApplyForcedOverrides(c, "", []core.ClientCapabilityOverride{o})
	if got.DVProfiles == nil || len(got.DVProfiles) != 0 || len(ClientPolicyFor(got).DolbyVisionProfiles) != 0 {
		t.Fatal("explicit empty profiles lost")
	}
	if !reflect.DeepEqual(c.DVProfiles, []int{5, 8}) {
		t.Fatal("runtime input mutated")
	}
}
