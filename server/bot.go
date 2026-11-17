// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
)

const (
	// defaultBotIcon is the bot's icon in the bundle.
	defaultBotIcon = "assets/icon.png"
	// defaultBotName is the bot's display name when the setting is empty.
	defaultBotName = "tix"
	// maxBotName caps the display name (Mattermost allows 64 runes).
	maxBotName = 64
	// maxIconBytes and maxIconSide bound a custom icon.
	maxIconBytes = 1 << 20
	maxIconSide  = 1024
	// botFeature namespaces the cached custom icon in the store.
	botFeature = "bot"
)

// botIcon is the custom icon last fetched, kept so a restart re-applies it without fetching again.
type botIcon struct {
	URL string `json:"url"`
	PNG []byte `json:"png"`
}

// botDisplayName is the configured display name, or tix.
func botDisplayName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return defaultBotName
	}
	if utf8.RuneCountInString(s) > maxBotName {
		s = string([]rune(s)[:maxBotName])
	}
	return s
}

// applyBotSettings renames the bot and sets its icon from the configuration, in the background:
// the icon may need a download, and a configuration change must not wait for it. The icon is
// fetched only when the URL changes; the bytes are kept for the next activation.
func (p *Plugin) applyBotSettings() {
	idp := p.botID.Load()
	if idp == nil || p.app == nil {
		return
	}
	botID, cfg := *idp, p.getConfiguration()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				p.API.LogError("tix: panic while applying the bot settings", "panic", fmt.Sprint(r))
			}
		}()
		p.botMu.Lock()
		defer p.botMu.Unlock()
		p.applyBotName(botID, botDisplayName(cfg.BotDisplayName))
		p.applyBotIcon(botID, strings.TrimSpace(cfg.BotIconURL))
	}()
}

func (p *Plugin) applyBotName(botID, name string) {
	bot, appErr := p.API.GetBot(botID, true)
	if appErr != nil || bot.DisplayName == name {
		return
	}
	if _, appErr := p.API.PatchBot(botID, &model.BotPatch{DisplayName: &name}); appErr != nil {
		p.API.LogWarn("tix: renaming the bot", "err", appErr.Error())
	}
}

func (p *Plugin) applyBotIcon(botID, iconURL string) {
	var rec botIcon
	if err := p.app.Store.Get(botFeature, "icon", &rec); err != nil {
		p.API.LogWarn("tix: reading the bot icon record", "err", err.Error())
		return
	}
	switch {
	case iconURL == "":
		if rec.URL == "" {
			return
		}
		if err := p.setDefaultBotIcon(botID); err != nil {
			p.API.LogWarn("tix: restoring the bot icon", "err", err.Error())
			return
		}
		_, _ = p.app.Store.Set(botFeature, "icon", nil)
		p.iconApplied = ""
		return
	case rec.URL == iconURL && len(rec.PNG) > 0:
		if p.iconApplied == iconURL {
			return
		}
	default:
		data, err := (&iconFetcher{}).fetch(iconURL)
		if err != nil {
			p.API.LogWarn("tix: not using the bot icon URL", "err", err.Error())
			return
		}
		rec = botIcon{URL: iconURL, PNG: data}
		if _, err := p.app.Store.Set(botFeature, "icon", rec); err != nil {
			p.API.LogWarn("tix: saving the bot icon", "err", err.Error())
		}
	}
	if appErr := p.API.SetProfileImage(botID, rec.PNG); appErr != nil {
		p.API.LogWarn("tix: setting the bot icon", "err", appErr.Error())
		return
	}
	p.iconApplied = iconURL
}

func (p *Plugin) setDefaultBotIcon(botID string) error {
	bundle, err := p.API.GetBundlePath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(bundle, defaultBotIcon)) //nolint:gosec // a constant path inside the bundle
	if err != nil {
		return err
	}
	if appErr := p.API.SetProfileImage(botID, data); appErr != nil {
		return appErr
	}
	return nil
}

// iconFetcher downloads a custom bot icon: https only, never from a private, loopback or
// link-local address (the URL is typed by an admin, but must not turn the plugin into a way to
// reach internal services), at most 1 MiB, and only a PNG of at most 1024x1024.
type iconFetcher struct {
	// allowPrivate and tlsConfig are for tests against a local TLS server.
	allowPrivate bool
	tlsConfig    *tls.Config
}

var errPrivateAddress = errors.New("the icon URL points at a private address")

func publicIP(ip net.IP) bool {
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsInterfaceLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified()
}

func (f *iconFetcher) client() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if !f.allowPrivate {
		// Checked on the address actually dialed, so a DNS answer cannot swap in a private one.
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || !publicIP(ip) {
				return errPrivateAddress
			}
			return nil
		}
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext:     dialer.DialContext,
			TLSClientConfig: f.tlsConfig,
			Proxy:           nil,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return errors.New("the icon URL redirects away from https")
			}
			if len(via) >= 3 {
				return errors.New("the icon URL redirects too often")
			}
			return nil
		},
	}
}

func (f *iconFetcher) fetch(raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("the icon URL must be an https URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching the icon: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching the icon: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading the icon: %w", err)
	}
	if len(data) > maxIconBytes {
		return nil, errors.New("the icon is larger than 1 MiB")
	}
	return data, checkIcon(data)
}

// checkIcon accepts a PNG of at most maxIconSide pixels a side.
func checkIcon(data []byte) error {
	if http.DetectContentType(data) != "image/png" {
		return errors.New("the icon is not a PNG")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return errors.New("the icon is not a valid PNG")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxIconSide || cfg.Height > maxIconSide {
		return fmt.Errorf("the icon is %dx%d; at most %dx%d works", cfg.Width, cfg.Height, maxIconSide, maxIconSide)
	}
	return nil
}
