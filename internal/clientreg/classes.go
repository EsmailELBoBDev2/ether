package clientreg

import (
	"strings"
	"sync"

	"github.com/braedonsaunders/ether/internal/core"
)

// Device classes group clients for quality rules.
const (
	ClassTV       = "tv"
	ClassMobile   = "mobile"
	ClassComputer = "computer"
)

// ClassInfo names one device class.
type ClassInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Classes lists the device classes in display order.
func Classes() []ClassInfo {
	return []ClassInfo{
		{ID: ClassTV, Name: "TVs & streaming boxes"},
		{ID: ClassMobile, Name: "Phones & tablets"},
		{ID: ClassComputer, Name: "Computers & browsers"},
	}
}

// Class is the device class of a client: the playback record's class,
// else one read from the platform, else the policy record's; "" when the
// client cannot be placed.
func Class(id Identity) string {
	if c := first("playback", id).Class; c != "" {
		return c
	}
	switch strings.ToLower(strings.TrimSpace(id.Platform)) {
	case "tvos", "roku", "webos", "tizen", "xbox", "playstation":
		return ClassTV
	case "ios", "ipados":
		return ClassMobile
	case "macos", "windows", "linux", "desktop":
		return ClassComputer
	}
	if core.IsFireTV(id.Product, id.Device) {
		return ClassTV
	}
	return first("policy", id).Class
}

// DeviceTypeInfo is one registry device type a rule can name.
type DeviceTypeInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group"`
}

type deviceSignature struct {
	name, policy, class string
}

var deviceSignatures = sync.OnceValue(func() []deviceSignature {
	out := make([]deviceSignature, 0, len(loaded.Devices))
	for _, d := range loaded.Devices {
		out = append(out, deviceSignature{name: d.Name, policy: first("policy", d.Identity).ID, class: Class(d.Identity)})
	}
	return out
})

// DeviceTypes lists the registry device types (the catalogue devices),
// each with its class. The id is the registry device name.
func DeviceTypes() []DeviceTypeInfo {
	sigs := deviceSignatures()
	out := make([]DeviceTypeInfo, 0, len(sigs))
	for _, s := range sigs {
		out = append(out, DeviceTypeInfo{ID: s.name, Name: s.name, Group: s.class})
	}
	return out
}

// DeviceType is the registry device type a client is, by its policy
// record and class, or "" when none fits. Fire TV is recognised by its
// model codes, which no catalogue identity carries.
func DeviceType(id Identity) string {
	if core.IsFireTV(id.Product, id.Device) {
		return "Fire TV"
	}
	policy, class := first("policy", id).ID, Class(id)
	if policy == "generic" {
		return ""
	}
	for _, s := range deviceSignatures() {
		if s.policy == policy && (class == "" || s.class == "" || s.class == class) {
			return s.name
		}
	}
	return ""
}

// AppID is the opaque id a quality rule uses for a registry app:
// "<protocol>:<name>".
func AppID(a App) string { return a.Protocol + ":" + a.Name }

// AppFor is the registry app a client is, by protocol and product, or ""
// when the registry does not list it.
func AppFor(protocol string, id Identity) string {
	product := strings.TrimSpace(id.Product)
	if product == "" {
		return ""
	}
	for _, a := range loaded.Apps {
		if protocol != "" && !strings.EqualFold(a.Protocol, protocol) {
			continue
		}
		for _, p := range a.AllProducts() {
			if strings.EqualFold(p, product) {
				return AppID(a)
			}
		}
	}
	return ""
}
