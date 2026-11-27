package mediabrowser

import (
	"fmt"
	"net/http"
	"strings"
)

// WebThemes are the looks Ether can give the web app it serves. Emby Web
// draws every surface from :root colour variables (hue, saturation and
// lightness parts), so a theme only sets those; the app's code and its own
// theme setting are untouched, and the server's custom CSS, which Emby Web
// loads for every viewer, carries it.
var WebThemes = []string{"ether", "amoled", "nord"}

// maxCustomCSS bounds the owner's own CSS (it is sent to every viewer).
const maxCustomCSS = 64 << 10

type webTheme struct {
	bgHue, bgSat, bgLight         float64
	accentHue, accentSat, accentL float64
}

var webThemes = map[string]webTheme{
	"ether":  {bgHue: 222, bgSat: 18, bgLight: 9, accentHue: 199, accentSat: 89, accentL: 52},
	"amoled": {bgHue: 0, bgSat: 0, bgLight: 0, accentHue: 152, accentSat: 64, accentL: 46},
	"nord":   {bgHue: 220, bgSat: 16, bgLight: 22, accentHue: 193, accentSat: 43, accentL: 60},
}

func (t webTheme) embyCSS() string {
	accent := fmt.Sprintf("hsl(%g, %g%%, %g%%)", t.accentHue, t.accentSat, t.accentL)
	lift := func(n float64) string { return fmt.Sprintf("%g%%", min(t.bgLight+n, 100)) }
	var b strings.Builder
	b.WriteString("/* Ether web theme */\nhtml:root, html:root.theme-light, html:root.theme-dark {\n")
	vars := [][2]string{
		{"color-scheme", "dark"},
		{"--background-hue", fmt.Sprint(t.bgHue)},
		{"--background-saturation", fmt.Sprintf("%g%%", t.bgSat)},
		{"--background-lightness", fmt.Sprintf("%g%%", t.bgLight)},
		{"--theme-primary-color-hue", fmt.Sprint(t.accentHue)},
		{"--theme-primary-color-saturation", fmt.Sprintf("%g%%", t.accentSat)},
		{"--theme-primary-color-lightness", fmt.Sprintf("%g%%", t.accentL)},
		{"--theme-text-color-hue", "0"},
		{"--theme-text-color-saturation", "0%"},
		{"--theme-text-color-lightness", "100%"},
		{"--theme-text-color-alpha", "1"},
		{"--theme-secondary-text-color-alpha", ".62"},
		{"--theme-accent-text-color-lightbg", accent},
		{"--theme-accent-text-color-darkbg", accent},
		{"--theme-accent-text-color", accent},
		{"--theme-accent-text-color-lightbg-alt", accent},
		{"--theme-accent-text-color-alt", accent},
		{"--button-background-hue", fmt.Sprint(t.bgHue)},
		{"--button-background-saturation", fmt.Sprintf("%g%%", t.bgSat)},
		{"--button-background-lightness", lift(12)},
		{"--button-background-alpha", ".7"},
		{"--card-background-hue", fmt.Sprint(t.bgHue)},
		{"--card-background-saturation", fmt.Sprintf("%g%%", t.bgSat)},
		{"--card-background-lightness", lift(7)},
		{"--card-background-alpha", ".9"},
		{"--input-background-hue", fmt.Sprint(t.bgHue)},
		{"--input-background-saturation", fmt.Sprintf("%g%%", t.bgSat)},
		{"--input-background-lightness", lift(10)},
		{"--focus-background-hue", fmt.Sprint(t.accentHue)},
		{"--focus-background-saturation", fmt.Sprintf("%g%%", t.accentSat)},
		{"--focus-background-lightness", fmt.Sprintf("%g%%", t.accentL)},
		{"--focus-blur-background-lightness", fmt.Sprintf("%g%%", t.accentL)},
		{"--focus-text-color-lightness", "100%"},
		{"--docked-drawer-background", fmt.Sprintf("hsla(%g, %g%%, %s, .92)", t.bgHue, t.bgSat, lift(3))},
		{"--line-background", "hsla(0, 0%, 100%, .12)"},
	}
	for _, v := range vars {
		fmt.Fprintf(&b, "  %s: %s;\n", v[0], v[1])
	}
	b.WriteString("}\n")
	return b.String()
}

// brandingCSS is the server's custom CSS: the chosen Ether theme (Emby Web)
// followed by the owner's own CSS, so the owner's rules win.
func (i *instance) brandingCSS() string {
	if i.host.Config == nil {
		return ""
	}
	cfg := i.host.Config()
	var parts []string
	if name, _ := cfg["webTheme"].(string); name != "" && i.protocol == "emby" {
		if t, ok := webThemes[name]; ok {
			parts = append(parts, t.embyCSS())
		}
	}
	if css, _ := cfg["customCss"].(string); strings.TrimSpace(css) != "" {
		parts = append(parts, css)
	}
	return strings.Join(parts, "\n")
}

func writeBrandingCSS(w http.ResponseWriter, css string) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(css))
}
