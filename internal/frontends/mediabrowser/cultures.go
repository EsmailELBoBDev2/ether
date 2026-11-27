package mediabrowser

import (
	"net/http"
	"sort"
	"sync"

	"golang.org/x/text/language"
)

// cultureCodes are the ISO 639-1 languages offered in the audio and subtitle
// language pickers. Media Browser clients store the three-letter code.
var cultureCodes = []string{
	"af", "am", "ar", "az", "bg", "bn", "bs", "ca", "cs", "cy", "da", "de", "el", "en", "eo",
	"es", "et", "eu", "fa", "fi", "fil", "fr", "ga", "gl", "gu", "he", "hi", "hr", "hu", "hy",
	"id", "is", "it", "ja", "ka", "kk", "km", "kn", "ko", "la", "lo", "lt", "lv", "mk", "ml",
	"mn", "mr", "ms", "my", "nb", "ne", "nl", "nn", "no", "pa", "pl", "pt", "ro", "ru", "sk",
	"sl", "sq", "sr", "sv", "sw", "ta", "te", "th", "tr", "uk", "ur", "uz", "vi", "zh", "zu",
}

type culture struct {
	Name                        string   `json:"Name"`
	DisplayName                 string   `json:"DisplayName"`
	TwoLetterISOLanguageName    string   `json:"TwoLetterISOLanguageName"`
	ThreeLetterISOLanguageName  string   `json:"ThreeLetterISOLanguageName"`
	ThreeLetterISOLanguageNames []string `json:"ThreeLetterISOLanguageNames"`
}

var cultureList = sync.OnceValue(func() []culture {
	names := englishLanguages()
	out := make([]culture, 0, len(cultureCodes))
	for _, code := range cultureCodes {
		tag := language.Make(code)
		base, _ := tag.Base()
		name := names.Name(tag)
		if name == "" {
			continue
		}
		three := base.ISO3()
		out = append(out, culture{Name: name, DisplayName: name, TwoLetterISOLanguageName: code, ThreeLetterISOLanguageName: three, ThreeLetterISOLanguageNames: []string{three}})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].DisplayName < out[b].DisplayName })
	return out
})

// cultures answers GET /Localization/Cultures, which Emby Web and Jellyfin
// Web need before their playback and subtitle settings pages render.
func (i *instance) cultures(w http.ResponseWriter, r *http.Request) {
	if v, _, err := i.viewer(r); err != nil || v == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}
	jsonReply(w, cultureList())
}
