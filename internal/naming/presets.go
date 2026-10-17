package naming

// Preset is a set of naming templates for a media server.
type Preset struct {
	Name  string `json:"name"`
	Movie string `json:"movie"`
	TV    string `json:"tv"`
	// ExtraDirs maps an extra type to the sub-folder name the server expects.
	ExtraDirs map[string]string `json:"extraDirs"`
}

// ExtraTypes are the supported movie extra categories.
var ExtraTypes = []string{"behindthescenes", "deleted", "featurette", "interview", "scene", "short", "trailer", "other"}

var plexStyleExtras = map[string]string{
	"behindthescenes": "Behind The Scenes",
	"deleted":         "Deleted Scenes",
	"featurette":      "Featurettes",
	"interview":       "Interviews",
	"scene":           "Scenes",
	"short":           "Shorts",
	"trailer":         "Trailers",
	"other":           "Other",
}

var jellyfinExtras = map[string]string{
	"behindthescenes": "Behind The Scenes",
	"deleted":         "Deleted Scenes",
	"featurette":      "Featurettes",
	"interview":       "Interviews",
	"scene":           "Scenes",
	"short":           "Shorts",
	"trailer":         "Trailers",
	"other":           "Extras",
}

// Presets are the built-in naming schemes. The default is "jellyfin".
var Presets = map[string]Preset{
	"jellyfin": {
		Name:      "jellyfin",
		Movie:     "{title}< ({year})> [tmdbid-{id}]/{title}< ({year})> [tmdbid-{id}]",
		TV:        "{show}< ({year})> [tmdbid-{id}]/Season {season:02}/{show} - S{season:02}E{episode:02}<-E{episode_end:02}>< - {episode_title}>",
		ExtraDirs: jellyfinExtras,
	},
	"plex": {
		Name:      "plex",
		Movie:     "{title}< ({year})> {{tmdb-{id}}}/{title}< ({year})> {{tmdb-{id}}}",
		TV:        "{show}< ({year})> {{tmdb-{id}}}/Season {season:02}/{show}< ({year})> - s{season:02}e{episode:02}<-e{episode_end:02}>< - {episode_title}>",
		ExtraDirs: plexStyleExtras,
	},
	"emby": {
		Name:      "emby",
		Movie:     "{title}< ({year})> [tmdbid={id}]/{title}< ({year})>",
		TV:        "{show}< ({year})> [tmdbid={id}]/Season {season:02}/{show} - S{season:02}E{episode:02}<-E{episode_end:02}>< - {episode_title}>",
		ExtraDirs: plexStyleExtras,
	},
}

// DefaultPreset is used until the user picks another one in the UI.
const DefaultPreset = "jellyfin"
