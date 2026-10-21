package capabilities

import (
	"regexp"
	"strings"
)

type BudgetRange struct {
	Min int64 `json:"min"`
	Max int64 `json:"max"`
	Set bool  `json:"-"`
}

type Capabilities struct {
	Vision                  bool        `json:"vision"`
	PDF                     bool        `json:"pdf"`
	AudioInput              bool        `json:"audioInput"`
	VideoInput              bool        `json:"videoInput"`
	ImageOutput             bool        `json:"imageOutput"`
	AudioOutput             bool        `json:"audioOutput"`
	Search                  bool        `json:"search"`
	Tools                   bool        `json:"tools"`
	Reasoning               bool        `json:"reasoning"`
	ThinkingFormat          string      `json:"thinkingFormat"`
	ThinkingCanDisable      bool        `json:"thinkingCanDisable"`
	ThinkingRange           BudgetRange `json:"thinkingRange"`
	ThinkingEffortSupported bool        `json:"thinkingEffortSupported"`
	ContextWindow           int64       `json:"contextWindow"`
	MaxOutput               int64       `json:"maxOutput"`
}

type patternEntry struct {
	Pattern string
	Caps    Capabilities
}

type Catalog struct {
	models            map[string]Capabilities
	providers         map[string]map[string]Capabilities
	patterns          []patternEntry
	matchers          []*regexp.Regexp
	notVision, vision *regexp.Regexp
}

// New freezes generated data and hand-written corrections for a snapshot.
func New() *Catalog {
	c := &Catalog{
		models:    make(map[string]Capabilities),
		providers: make(map[string]map[string]Capabilities),
		patterns:  append([]patternEntry(nil), patternData...),
		notVision: regexp.MustCompile("(?i)" + notVisionPattern),
		vision:    regexp.MustCompile("(?i)" + visionPattern),
	}
	for model, caps := range modelData {
		c.models[model] = caps
	}
	for provider, models := range providerData {
		c.providers[provider] = make(map[string]Capabilities, len(models))
		for model, caps := range models {
			c.providers[provider][model] = caps
		}
	}
	for _, pattern := range c.patterns {
		c.matchers = append(c.matchers, regexp.MustCompile("(?i)^"+strings.ReplaceAll(regexp.QuoteMeta(pattern.Pattern), `\*`, ".*")+"$"))
	}
	for provider, models := range overrides {
		if c.providers[provider] == nil {
			c.providers[provider] = make(map[string]Capabilities)
		}
		for model, caps := range models {
			c.providers[provider][model] = caps
		}
	}
	return c
}

func (c *Catalog) Resolve(provider, model string) Capabilities {
	if model == "" {
		return defaults
	}
	if canonical, ok := aliases[provider]; ok {
		provider = canonical
	}
	base := model[strings.LastIndex(model, "/")+1:]
	if caps, ok := c.providers[provider][model]; ok {
		return caps
	}
	if caps, ok := c.providers[provider][base]; ok {
		return caps
	}
	if provider == "commandcode" || provider == "cmc" {
		caps := commandDefault
		key := strings.ToLower(model)
		for _, id := range commandTextOnly {
			bare := id[strings.LastIndex(id, "/")+1:]
			if key == id || key == bare || strings.HasSuffix(key, "/"+bare) {
				caps.Vision = false
				break
			}
		}
		return caps
	}
	caps, ok := c.models[base]
	if !ok {
		caps, ok = c.models[model]
	}
	if !ok {
		caps = defaults
		for i, pattern := range c.patterns {
			if c.matchers[i].MatchString(base) || c.matchers[i].MatchString(model) {
				caps = pattern.Caps
				break
			}
		}
	}
	if !caps.Vision && !c.notVision.MatchString(model) && c.vision.MatchString(model) {
		caps.Vision = true
	}
	return caps
}

var builtins = New()

func Resolve(provider, model string) Capabilities { return builtins.Resolve(provider, model) }
