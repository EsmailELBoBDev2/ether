package config

// Plex media delivery modes (plex.delivery).
const (
	// DeliveryRelay streams media bytes through Ether.
	DeliveryRelay = "relay"
	// DeliveryRedirect answers an authorised direct-play part read with a 307
	// to the provider's own URL, when the session has one.
	DeliveryRedirect = "redirect"
)

// DeliveryOrDefault is how Plex media routes deliver to clients. Only an
// explicit "redirect" redirects: an unset or unrecognised value relays, which
// is the behaviour every other frontend and every older config expects.
func (p Plex) DeliveryOrDefault() string {
	if p.Delivery == DeliveryRedirect {
		return DeliveryRedirect
	}
	return DeliveryRelay
}
