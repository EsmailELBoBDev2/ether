package app

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/plextv"
)

// Is lets the console tell an account already in use from other failures.
func (e errPlexAccountInUse) Is(target error) bool { return target == admin.ErrPlexAccountInUse }

// plexServerView is one server as the console shows it.
type plexServerView struct {
	ID            string `json:"id"`
	Main          bool   `json:"main"`
	Name          string `json:"name"`
	NameInherited bool   `json:"nameInherited"`
	Account       string `json:"account,omitempty"`
	State         string `json:"state"`
	Running       bool   `json:"running"`
	Claimed       bool   `json:"claimed"`
	SeatsUsed     int    `json:"seatsUsed"`
	AddedAt       int64  `json:"addedAt,omitempty"`
	ActivatedAt   int64  `json:"activatedAt,omitempty"`
}

// plexServersView is the console's Plex servers panel and the dashboard
// section the capacity warning reads.
type plexServersView struct {
	SeatsPerServer int              `json:"seatsPerServer"`
	SeatWarning    int              `json:"seatWarning"`
	SeatsUsed      int              `json:"seatsUsed"`
	SeatsTotal     int              `json:"seatsTotal"`
	Servers        []plexServerView `json:"servers"`
	// AlmostFull: every active server has reached SeatWarning.
	AlmostFull bool `json:"almostFull"`
	// Standby counts signed-in accounts waiting to start.
	Standby int `json:"standby"`
	// Warning: almost full and nothing left to start; add a Plex server.
	Warning bool `json:"warning"`
	// Claimed: the main server is linked to plex.tv (extra servers need it).
	Claimed bool `json:"claimed"`
}

// recentActivation is how long the console notes that a standby server
// started on its own.
const recentActivation = 7 * 24 * time.Hour

func (a *App) plexServersStatus(ctx context.Context) any {
	if a.plexServers == nil {
		return nil
	}
	return a.plexServers.view(ctx)
}

func (ps *plexServers) view(ctx context.Context) plexServersView {
	cfg := ps.app.cfg.Get()
	used := ps.seatsUsed(ctx)
	out := plexServersView{
		SeatsPerServer: cfg.PlexSeatsPerServer(),
		SeatWarning:    cfg.PlexSeatWarning(),
		Claimed:        cfg.Plex.Claimed,
	}
	out.Servers = append(out.Servers, plexServerView{
		ID: "", Main: true, Name: cfg.Server.FriendlyName, Account: cfg.Plex.OwnerUsername,
		State: config.PlexServerActive, Running: cfg.Plex.Claimed, Claimed: cfg.Plex.Claimed, SeatsUsed: used[""],
	})
	for _, s := range append(activePlexOrder(cfg), standbyPlexOrder(cfg)...) {
		v := plexServerView{
			ID: s.ID, Name: s.AdvertisedName(cfg), NameInherited: s.Name == "", Account: s.Account.Username,
			State: s.State, Running: ps.runtime(s.ID) != nil, Claimed: s.Claimed, SeatsUsed: used[s.ID],
			AddedAt: s.AddedAt, ActivatedAt: s.ActivatedAt,
		}
		if !s.Active() {
			out.Standby++
		}
		out.Servers = append(out.Servers, v)
	}
	for _, v := range out.Servers {
		if v.State == config.PlexServerActive {
			out.SeatsUsed += v.SeatsUsed
			out.SeatsTotal += out.SeatsPerServer
		}
	}
	out.AlmostFull = cfg.Plex.Claimed && ps.almostFull(ctx, cfg)
	out.Warning = out.AlmostFull && out.Standby == 0
	return out
}

// plexServersAdmin is the console API (admin.PlexServers).
type plexServersAdmin struct {
	app  *App
	mu   *sync.Mutex
	pins map[string]*plextv.PIN
}

func newPlexServersAdmin(a *App) plexServersAdmin {
	return plexServersAdmin{app: a, mu: &sync.Mutex{}, pins: map[string]*plextv.PIN{}}
}

func (p plexServersAdmin) Status(ctx context.Context) any { return p.app.plexServers.view(ctx) }

func (p plexServersAdmin) StartPIN(ctx context.Context) (id, code, authURL string, err error) {
	if p.app.registrar == nil {
		return "", "", "", errors.New("plex.tv is not available")
	}
	if !p.app.cfg.Get().Plex.Claimed {
		return "", "", "", errors.New("link the main Plex server first")
	}
	c := p.app.registrar.SignInClient()
	pin, err := c.CreatePIN(ctx)
	if err != nil {
		return "", "", "", err
	}
	id = strconv.FormatInt(pin.ID, 10)
	p.mu.Lock()
	rememberPIN(p.pins, id, pin)
	p.mu.Unlock()
	return id, pin.Code, c.AuthURL(pin, ""), nil
}

func (p plexServersAdmin) PollPIN(ctx context.Context, id string) (bool, any, error) {
	p.mu.Lock()
	pin := p.pins[id]
	p.mu.Unlock()
	if pin == nil {
		return false, nil, errors.New("unknown sign-in request; start again")
	}
	got, err := p.app.registrar.SignInClient().CheckPIN(ctx, pin.ID, pin.Code)
	if err != nil {
		return false, nil, err
	}
	if got.AuthToken == "" {
		return false, nil, nil
	}
	p.mu.Lock()
	delete(p.pins, id)
	p.mu.Unlock()
	added, err := p.app.plexServers.addAccount(ctx, got.AuthToken)
	if err != nil {
		return false, nil, err
	}
	return true, map[string]any{"id": added.ID, "account": added.Account.Username, "state": added.State}, nil
}

func (p plexServersAdmin) Start(ctx context.Context, id string) error {
	return p.app.plexServers.activate(ctx, id)
}

func (p plexServersAdmin) Rename(id, name string) error { return p.app.plexServers.rename(id, name) }

func (p plexServersAdmin) Remove(ctx context.Context, id string) error {
	return p.app.plexServers.remove(ctx, id)
}
