package plugin

import (
	"embed"
	"encoding/json"
	"log"
	"strings"

	sdkconfig "github.com/kumiho-plugin/kumiho-plugin-sdk/config"
)

//go:embed locales/*.json
var localeFS embed.FS

var supportedLocales = []string{"ko", "en", "ja"}

var localeBundle = loadLocaleBundle()

func loadLocaleBundle() map[string]map[string]string {
	bundle := make(map[string]map[string]string, len(supportedLocales))
	for _, locale := range supportedLocales {
		raw, err := localeFS.ReadFile("locales/" + locale + ".json")
		if err != nil {
			log.Printf("kitsu plugin locale load failed for %s: %v", locale, err)
			continue
		}

		var messages map[string]string
		if err := json.Unmarshal(raw, &messages); err != nil {
			log.Printf("kitsu plugin locale parse failed for %s: %v", locale, err)
			continue
		}
		bundle[locale] = messages
	}
	return bundle
}

func localized(key string, fallback string) sdkconfig.LocalizedString {
	result := make(sdkconfig.LocalizedString, len(supportedLocales))
	for _, locale := range supportedLocales {
		value := strings.TrimSpace(localeBundle[locale][key])
		if value == "" {
			continue
		}
		result[locale] = value
	}
	if len(result) == 0 && strings.TrimSpace(fallback) != "" {
		result["en"] = strings.TrimSpace(fallback)
	}
	return result
}
