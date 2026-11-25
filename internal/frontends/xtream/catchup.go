package xtream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/kit/caps"
)

func archiveStart(text string) (time.Time, error) {
	if epoch, err := strconv.ParseInt(text, 10, 64); err == nil {
		return time.Unix(epoch, 0).UTC(), nil
	}
	for _, layout := range []string{"2006-01-02:15-04", "2006-01-02:15:04", "2006-01-02 15:04:05"} {
		if start, err := time.Parse(layout, text); err == nil {
			return start, nil
		}
	}
	return time.Time{}, frontend.ErrInvalidCatchUpRange
}

func (i *instance) catchUp(w http.ResponseWriter, r *http.Request, l login) {
	host, ok := i.host.LiveTV.(frontend.LiveCatchUp)
	if !ok || i.host.Capabilities == nil {
		http.Error(w, "Unavailable", 503)
		return
	}
	file, duration, start := r.PathValue("file"), r.PathValue("duration"), r.PathValue("start")
	if duration == "" {
		query := r.URL.Query()
		file, duration, start = query.Get("stream"), query.Get("duration"), query.Get("start")
		if file == "" {
			file = query.Get("stream_id")
		}
	}
	id, ext, hasExtension := strings.Cut(file, ".")
	if hasExtension && ext != "ts" {
		http.NotFound(w, r)
		return
	}
	minutes, err := strconv.Atoi(duration)
	if err != nil || minutes <= 0 || minutes > 30*24*60 {
		http.Error(w, "Invalid archive range", 400)
		return
	}
	at, err := archiveStart(start)
	if err != nil {
		http.Error(w, "Invalid archive range", 400)
		return
	}
	channel, ok := i.findChannel(r, l, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ip, ok := i.host.ClientIP(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256([]byte(l.device.ID + "/" + ip.String() + "/timeshift/" + id))
	device := l.device
	device.ID += "-" + hex.EncodeToString(sum[:12])
	ctx := frontend.WithClientIP(r.Context(), ip)
	profile, err := i.host.Capabilities.Build(ctx, frontend.CapabilityRequest{Viewer: l.viewer, Device: caps.DeviceInfo{
		Protocol: "xtream", ClientID: device.ID, Product: device.Product, MaxHeight: 4320, HDR10: true,
		VideoCodecs: []string{"h264", "hevc"}, AudioCodecs: []string{"aac", "ac3", "eac3", "mp3"}, Containers: []string{"mpegts", "ts"},
	}, UserAgent: r.UserAgent()})
	if err != nil {
		http.Error(w, "Unavailable", 503)
		return
	}
	client := profile.Client
	client.HLSContainers = []string{"mpegts"}
	req := frontend.CatchUpRequest{ChannelID: channel.ID, Start: at, Duration: time.Duration(minutes) * time.Minute}
	if !i.admit(w, r.WithContext(ctx), l, frontend.PlanRequest{Viewer: l.viewer, Device: device, Protocol: "xtream", Channel: channel.ID, Caps: client}) {
		return
	}
	info, err := host.StartCatchUp(ctx, l.viewer, req, client, device.ID)
	if err != nil {
		var archive *frontend.CatchUpError
		status := http.StatusServiceUnavailable
		switch {
		case errors.Is(err, frontend.ErrNotFound), errors.Is(err, frontend.ErrUnauthorized):
			status = http.StatusNotFound
		case errors.Is(err, frontend.ErrStreamLimit):
			status = http.StatusTooManyRequests
		case errors.As(err, &archive):
			status = http.StatusConflict
			if archive.Code == "catchup_unavailable" {
				status = http.StatusBadGateway
			}
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	defer i.host.LiveTV.Stop(context.WithoutCancel(ctx), info.ID, l.viewer)
	host.ServeCatchUpTS(w, r.WithContext(ctx), info.ID, l.viewer)
}
