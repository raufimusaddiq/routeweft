package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// V1Item names one v1 value and why it is not carried. Path names the value,
// never its content: no item holds a key, token, password or proxy URL.
type V1Item struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// V1KeyReissue lists a v1 client API key by name and access only. The secret is
// never read into the plan; the operator reissues the key (docs/V1_MIGRATION.md §5).
type V1KeyReissue struct {
	Name       string   `json:"name"`
	Active     bool     `json:"active"`
	Restricted bool     `json:"restricted"`
	Allow      []string `json:"allow,omitempty"`
}

// V1Reauth lists a v1 provider connection the operator must authorize again.
type V1Reauth struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Name     string `json:"name,omitempty"`
	AuthType string `json:"authType"`
	Active   bool   `json:"active"`
	Priority int    `json:"priority,omitempty"`
	Action   string `json:"action"`
}

// V1Plan is the outcome of the one-time v1 policy (docs/V1_MIGRATION.md). It
// is a plan, not a commit: Settings goes through SetSettings, Combos and
// Aliases through ApplyCatalog, so each still passes the normal
// validate-compile-commit-publish protocol.
type V1Plan struct {
	Settings map[string]string   `json:"settings"`
	Combos   []Combo             `json:"-"`
	Aliases  map[string]ModelRef `json:"-"`
	// Notes are carried values with a caveat (unit conversion, default kept).
	Notes []string `json:"notes,omitempty"`
	// Refused values are named and never stored.
	Refused    []V1Item       `json:"refused,omitempty"`
	KeyReissue []V1KeyReissue `json:"keyReissue,omitempty"`
	Reauth     []V1Reauth     `json:"reauth,omitempty"`
	// Recreate lists configuration the operator rebuilds by hand.
	Recreate []V1Item `json:"recreate,omitempty"`
}

const (
	v1HistoryReason  = "v1 usage and request history is not imported (docs/V1_MIGRATION.md §7); keep the v1 file read-only outside Routeweft"
	v1ReauthOAuth    = "authorize again through the Routeweft OAuth flow; v1 tokens are not read"
	v1ReauthAPIKey   = "enter the provider API key again in Routeweft; the v1 key is not read"
	v1ReauthMissing  = "authorize again; the v1 auth type is unknown to Routeweft"
	v1ComboNeedsBase = "recreate its providers first, then re-run the plan"
)

// v1Tables are the top-level members of the pinned JSON export
// (src/lib/db/index.js:78-88).
var v1Tables = map[string]struct{}{
	"settings": {}, "providerConnections": {}, "providerNodes": {}, "proxyPools": {},
	"apiKeys": {}, "combos": {}, "modelAliases": {}, "customModels": {}, "pricing": {},
}

var v1HistoryTables = map[string]struct{}{"usageHistory": {}, "usageDaily": {}, "requestDetails": {}}

// v1PassthroughSettings are settings whose key, type and unit are identical in
// Routeweft (milliseconds, characters, counts); the value is validated by
// validateSettingValue before it is carried.
var v1PassthroughSettings = map[string]string{
	"rtkEnabled": "bool", "headroomEnabled": "bool", "headroomUrl": "string",
	"headroomCompressUserMessages": "bool", "headroomTimeoutMs": "uint",
	"cavemanEnabled": "bool", "cavemanLevel": "string",
	"ponytailEnabled": "bool", "ponytailLevel": "string",
	"pxpipeEnabled": "bool", "pxpipeAutoInstall": "bool", "pxpipeMinChars": "uint", "pxpipeTimeoutMs": "uint",
	"enableObservability": "bool", "observabilityMaxRecords": "uint", "observabilityBatchSize": "uint",
	"observabilityFlushIntervalMs": "uint", "outboundProxyEnabled": "bool", "outboundProxyUrl": "string",
	"requireApiKey": "bool", "quotaVisibility": "object",
}

// v1UnitDefault are settings the pin reads with `|| default`, so 0 means unset.
var v1UnitDefault = map[string]struct{}{
	"observabilityMaxRecords": {}, "observabilityBatchSize": {}, "observabilityFlushIntervalMs": {},
}

// v1C04Settings are owned by ImportPinSettings, which reports its own notes.
var v1C04Settings = map[string]struct{}{
	"stickyRoundRobinLimit": {}, "comboStickyRoundRobinLimit": {}, "capacityAdapter": {},
	"outboundNoProxy": {}, "observabilityMaxJsonSize": {}, "dnsToolEnabled": {},
	"fallbackStrategy": {}, "comboStrategy": {}, "providerStrategies": {}, "comboStrategies": {},
	"ccFilterNaming": {}, "headroomCodeAware": {}, "headroomKompress": {},
}

// v1RefusedSettings carry a specific reason; any other unknown key gets the
// generic one. Reasons never include the value.
var v1RefusedSettings = map[string]string{
	"password":      "the dashboard password hash is not carried; set the Routeweft bootstrap password",
	"cloudEnabled":  "Routeweft has no cloud sync (PRD 4.2)",
	"tunnelEnabled": "removed by v1 itself", "tunnelUrl": "removed by v1 itself", "tunnelProvider": "removed by v1 itself",
	"tailscaleEnabled": "removed by v1 itself", "tailscaleUrl": "removed by v1 itself", "tunnelDashboardAccess": "removed by v1 itself",
	"mitmRouterBaseUrl": "removed by v1 itself", "mitmSudoEncrypted": "removed by v1 itself",
}

// PlanV1Migration applies the v1 policy to a pinned JSON export (the document
// GET /api/settings/database returns). It reads no database and keeps no
// secret: apiKeys[].key, connection credentials, proxy URLs and the password
// are never copied, only named. A malformed document fails as a whole; a
// malformed value inside a well-formed one is refused by name.
func PlanV1Migration(export []byte) (*V1Plan, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(export, &document); err != nil || document == nil {
		return nil, fmt.Errorf("v1 export must be a JSON object (the document GET /api/settings/database returns); a data.sqlite file is not accepted")
	}
	plan := &V1Plan{Settings: map[string]string{}, Aliases: map[string]ModelRef{}}
	refuse := func(path, format string, args ...any) {
		plan.Refused = append(plan.Refused, V1Item{Path: path, Reason: fmt.Sprintf(format, args...)})
	}
	recreate := func(path, reason string) { plan.Recreate = append(plan.Recreate, V1Item{Path: path, Reason: reason}) }

	for name := range document {
		if _, ok := v1Tables[name]; ok {
			continue
		}
		if _, ok := v1HistoryTables[name]; ok {
			refuse(name, "%s", v1HistoryReason)
			continue
		}
		refuse(name, "not part of the v1 export; not read")
	}

	var settings map[string]json.RawMessage
	if raw, ok := document["settings"]; ok {
		if json.Unmarshal(raw, &settings) != nil || settings == nil {
			return nil, fmt.Errorf("settings must be an object")
		}
		if err := plan.planSettings(settings, refuse); err != nil {
			return nil, err
		}
	}
	plan.planKeys(document["apiKeys"], refuse)
	plan.planConnections(document["providerConnections"], refuse)
	plan.planNodesAndPools(document, recreate, refuse)
	plan.planAliases(document["modelAliases"], refuse)
	plan.planCombos(document["combos"], settings, refuse)
	plan.dropSupersededFusionNotes()

	if raw, ok := document["customModels"]; ok && !isEmptyJSON(raw) {
		recreate("customModels", "recreate each model by hand; v1 keys it by provider alias, which is not a Routeweft provider ID")
	}
	if raw, ok := document["pricing"]; ok && !isEmptyJSON(raw) {
		recreate("pricing", "re-enter overrides by hand: same $/1M-token unit, but cached->cache read, cache_creation->cache write, and reasoning has no Routeweft rate")
	}

	sort.Strings(plan.Notes)
	sort.Slice(plan.Refused, func(i, j int) bool { return plan.Refused[i].Path < plan.Refused[j].Path })
	sort.Slice(plan.Recreate, func(i, j int) bool { return plan.Recreate[i].Path < plan.Recreate[j].Path })
	return plan, nil
}

func (p *V1Plan) planSettings(pin map[string]json.RawMessage, refuse func(string, string, ...any)) error {
	carried, notes, err := ImportPinSettings(pin)
	if err != nil {
		// One malformed C04 value must not fail the plan. Retry without the
		// offending key so the remaining settings are still planned; the error
		// names a key, never a value.
		bad := v1BadKey(pin)
		if bad == "" {
			return err
		}
		refuse("settings."+bad, "is malformed (%v), not carried", err)
		rest := make(map[string]json.RawMessage, len(pin))
		for key, value := range pin {
			if key != bad {
				rest[key] = value
			}
		}
		return p.planSettings(rest, refuse)
	}
	for key, value := range carried {
		p.Settings[key] = value
	}
	p.Notes = append(p.Notes, notes...)
	for key, raw := range pin {
		path := "settings." + key
		if _, ok := v1C04Settings[key]; ok {
			continue
		}
		kind, passthrough := v1PassthroughSettings[key]
		if !passthrough {
			p.refuseUnmapped(key, raw, refuse)
			continue
		}
		value, ok := v1SettingText(kind, raw)
		if !ok {
			refuse(path, "is not a valid %s, not carried", kind)
			continue
		}
		if _, defaulted := v1UnitDefault[key]; defaulted && value == "0" {
			p.Notes = append(p.Notes, fmt.Sprintf("%s: 0 means unset in the pin, keeping the Routeweft default", key))
			continue
		}
		if key == "outboundProxyUrl" && value != "" {
			if parsed, err := url.Parse(value); err == nil && parsed.User != nil {
				refuse(path, "carries proxy credentials; enter the proxy by hand so the secret is not copied")
				continue
			}
		}
		if err := validateSettingValue(key, value); err != nil {
			refuse(path, "is rejected by Routeweft's validation, not carried")
			continue
		}
		p.Settings[key] = value
	}
	return nil
}

// v1BadKey finds the one C04 key ImportPinSettings fails on, by probing each
// key alone. It returns "" when no single key is responsible.
func v1BadKey(pin map[string]json.RawMessage) string {
	for key, value := range pin {
		if _, ok := v1C04Settings[key]; !ok {
			continue
		}
		if _, _, err := ImportPinSettings(map[string]json.RawMessage{key: value}); err != nil {
			return key
		}
	}
	return ""
}

func (p *V1Plan) refuseUnmapped(key string, raw json.RawMessage, refuse func(string, string, ...any)) {
	path := "settings." + key
	switch key {
	case "requireLogin":
		var on bool
		if json.Unmarshal(raw, &on) == nil && on {
			return // the Routeweft default and only mode
		}
		refuse(path, "Routeweft always requires a session (C03); login cannot be disabled")
	case "cloudEnabled":
		var on bool
		if json.Unmarshal(raw, &on) == nil && !on {
			return
		}
		refuse(path, "%s", v1RefusedSettings[key])
	default:
		if reason, ok := v1RefusedSettings[key]; ok {
			refuse(path, "%s", reason)
			return
		}
		refuse(path, "has no Routeweft equivalent, not stored")
	}
}

// v1SettingText renders a JSON value as the string SetSettings takes.
func v1SettingText(kind string, raw json.RawMessage) (string, bool) {
	switch kind {
	case "bool":
		var v bool
		if json.Unmarshal(raw, &v) != nil || string(bytes.TrimSpace(raw)) == "null" {
			return "", false
		}
		return fmt.Sprint(v), true
	case "uint":
		v, ok := pinUint(raw)
		return formatUint(v), ok
	case "string":
		var v string
		if json.Unmarshal(raw, &v) != nil || string(bytes.TrimSpace(raw)) == "null" {
			return "", false
		}
		return v, true
	default: // object
		var v map[string]json.RawMessage
		if json.Unmarshal(raw, &v) != nil || v == nil {
			return "", false
		}
		return string(bytes.TrimSpace(raw)), true
	}
}

func (p *V1Plan) planKeys(raw json.RawMessage, refuse func(string, string, ...any)) {
	var keys []struct {
		Name     string `json:"name"`
		IsActive *bool  `json:"isActive"`
		Access   *struct {
			Restricted bool     `json:"restricted"`
			Allow      []string `json:"allow"`
		} `json:"access"`
	}
	if len(raw) == 0 {
		return
	}
	if json.Unmarshal(raw, &keys) != nil {
		refuse("apiKeys", "is not an array of keys; none reissued, none read")
		return
	}
	if len(keys) == 0 {
		return
	}
	for i, key := range keys {
		entry := V1KeyReissue{Name: key.Name, Active: key.IsActive == nil || *key.IsActive}
		if entry.Name == "" {
			entry.Name = fmt.Sprintf("apiKeys[%d]", i)
		}
		if key.Access != nil {
			entry.Restricted, entry.Allow = key.Access.Restricted, key.Access.Allow
		}
		p.KeyReissue = append(p.KeyReissue, entry)
	}
	refuse("apiKeys", "v1 stores client keys in plaintext and Routeweft only a digest; no key is carried. Reissue %d key(s) from the list and give clients the new value", len(keys))
}

func (p *V1Plan) planConnections(raw json.RawMessage, refuse func(string, string, ...any)) {
	var rows []struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		AuthType string `json:"authType"`
		Name     string `json:"name"`
		Priority int    `json:"priority"`
		Active   *bool  `json:"isActive"`
	}
	if len(raw) == 0 {
		return
	}
	if json.Unmarshal(raw, &rows) != nil {
		refuse("providerConnections", "is not an array of connections; none listed, none read")
		return
	}
	if len(rows) == 0 {
		return
	}
	for _, row := range rows {
		action := v1ReauthMissing
		switch row.AuthType {
		case "oauth":
			action = v1ReauthOAuth
		case "apikey":
			action = v1ReauthAPIKey
		}
		p.Reauth = append(p.Reauth, V1Reauth{ID: row.ID, Provider: row.Provider, Name: row.Name, AuthType: row.AuthType, Active: row.Active == nil || *row.Active, Priority: row.Priority, Action: action})
	}
	refuse("providerConnections", "credentials are never copied out of a v1 export (D03); %d connection(s) listed for re-authorization", len(rows))
}

func (p *V1Plan) planNodesAndPools(document map[string]json.RawMessage, recreate func(string, string), refuse func(string, string, ...any)) {
	for _, table := range []struct{ name, reason string }{
		{"providerNodes", "recreate each compatible/custom node by hand"},
		{"proxyPools", "recreate each pool by hand; pool URLs carry credentials, so none is read"},
	} {
		raw, ok := document[table.name]
		if !ok || isEmptyJSON(raw) {
			continue
		}
		var rows []struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &rows) != nil {
			refuse(table.name, "is not an array, not read")
			continue
		}
		for _, row := range rows {
			recreate(fmt.Sprintf("%s[%s]", table.name, row.ID), table.reason)
		}
	}
}

func (p *V1Plan) planAliases(raw json.RawMessage, refuse func(string, string, ...any)) {
	if len(raw) == 0 {
		return
	}
	var aliases map[string]json.RawMessage
	if json.Unmarshal(raw, &aliases) != nil || aliases == nil {
		refuse("modelAliases", "is not an object, not read")
		return
	}
	for alias, value := range aliases {
		var target string
		provider, model, ok := "", "", false
		if json.Unmarshal(value, &target) == nil {
			provider, model, ok = strings.Cut(strings.TrimSpace(target), "/")
		}
		if !ok || provider == "" || model == "" || strings.TrimSpace(alias) == "" {
			refuse("modelAliases."+alias, "target is not a \"provider/model\" string, not carried")
			continue
		}
		p.Aliases[alias] = ModelRef{ProviderID: provider, ModelID: model}
	}
}

func (p *V1Plan) planCombos(raw json.RawMessage, settings map[string]json.RawMessage, refuse func(string, string, ...any)) {
	if len(raw) == 0 {
		return
	}
	var rows []struct {
		Name   string            `json:"name"`
		Kind   string            `json:"kind"`
		Models []json.RawMessage `json:"models"`
	}
	if json.Unmarshal(raw, &rows) != nil {
		refuse("combos", "is not an array of combos, not read")
		return
	}
	var perCombo map[string]json.RawMessage
	if raw, ok := settings["comboStrategies"]; ok {
		_ = json.Unmarshal(raw, &perCombo) // a malformed map was already an error in ImportPinSettings
	}
	for _, row := range rows {
		path := "combos." + row.Name
		if strings.TrimSpace(row.Name) == "" {
			refuse("combos", "a combo has no name, not carried")
			continue
		}
		combo := Combo{ID: "v1-" + row.Name, Name: row.Name}
		bad := false
		for i, item := range row.Models {
			var text string
			var object struct {
				Model string `json:"model"`
			}
			switch {
			case json.Unmarshal(item, &text) == nil && text != "":
			case json.Unmarshal(item, &object) == nil && object.Model != "":
				text = object.Model
			default:
				refuse(path, "models[%d] is not a model string, combo not carried", i)
				bad = true
			}
			if bad {
				break
			}
			provider, model, ok := strings.Cut(text, "/")
			if !ok {
				provider, model = "", text // an alias name, resolved at compile
			}
			combo.Members = append(combo.Members, ComboMember{ProviderID: provider, ModelID: model, Position: len(combo.Members), Selected: true})
		}
		if bad {
			continue
		}
		if len(combo.Members) == 0 {
			refuse(path, "has no models and would route nowhere, combo not carried")
			continue
		}
		p.applyFusion(&combo, row.Kind, perCombo[row.Name], refuse)
		p.Combos = append(p.Combos, combo)
	}
}

// dropSupersededFusionNotes removes the settings importer's "not carried" notes
// for fusion fields that a carried Combo now holds, so the plan never says a
// value was dropped when it was not.
func (p *V1Plan) dropSupersededFusionNotes() {
	kept := p.Notes[:0]
	for _, note := range p.Notes {
		superseded := false
		for _, combo := range p.Combos {
			if !combo.FusionEnabled {
				continue
			}
			prefix := "comboStrategies." + combo.Name + ": "
			if strings.HasPrefix(note, prefix+`field "judgeModel"`) || strings.HasPrefix(note, prefix+`field "fusionTuning"`) || strings.HasPrefix(note, prefix+`fallbackStrategy has no Routeweft equivalent`) {
				superseded = true
			}
		}
		if !superseded {
			kept = append(kept, note)
		}
	}
	p.Notes = kept
}

// applyFusion reads the v1 per-combo fusion fields (open-sse/services/combo.js:
// 472-476, src/sse/handlers/chat.js:116-132). Fusion is selected by
// comboStrategies[name].fallbackStrategy == "fusion"; the combo's own `kind`
// column is not read by the pin's chat handler.
func (p *V1Plan) applyFusion(combo *Combo, kind string, raw json.RawMessage, refuse func(string, string, ...any)) {
	path := "combos." + combo.Name
	if kind != "" && kind != "fusion" {
		p.Notes = append(p.Notes, fmt.Sprintf("%s: kind is not read by Routeweft", path))
	}
	var entry struct {
		Strategy string          `json:"fallbackStrategy"`
		Judge    string          `json:"judgeModel"`
		Tuning   json.RawMessage `json:"fusionTuning"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &entry) != nil {
		return
	}
	if entry.Strategy != "fusion" {
		return
	}
	combo.FusionEnabled, combo.JudgeModel = true, strings.TrimSpace(entry.Judge)
	if combo.JudgeModel == "" {
		p.Notes = append(p.Notes, path+": fusion has no judgeModel; the first panel member judges, as in the pin")
	}
	if len(entry.Tuning) == 0 || isEmptyJSON(entry.Tuning) {
		return
	}
	var tuning map[string]json.RawMessage
	if json.Unmarshal(entry.Tuning, &tuning) != nil || tuning == nil {
		refuse(path+".fusionTuning", "is not an object, default timing kept")
		return
	}
	var out routing.FusionTuning
	for key, value := range tuning {
		number, ok := pinUint(value)
		switch {
		case !ok && string(bytes.TrimSpace(value)) == "null":
		case !ok:
			refuse(path+".fusionTuning."+key, "is not a non-negative integer, default kept")
		case key == "minPanel":
			n := int(max(number, 2)) // below 2 behaves as 2 in the pin (PARITY C06)
			out.MinPanel = &n
		case key == "stragglerGraceMs":
			n := int64(number)
			out.StragglerGraceMs = &n
		case key == "panelHardTimeoutMs":
			n := int64(number)
			out.PanelHardTimeoutMs = &n
		default:
			refuse(path+".fusionTuning."+key, "is not a Routeweft fusion field, not carried")
		}
	}
	if err := out.Validate(); err != nil {
		refuse(path+".fusionTuning", "%v; default timing kept", err)
		return
	}
	combo.FusionTuning = out
}

// ApplyCatalog commits the plan's aliases and Combos one at a time through the
// normal compile-before-commit path. A member the catalog does not know yet
// fails only its own entry; the failure is returned as a refusal naming the
// entry, so recreating providers first and re-running loses nothing.
func (p *V1Plan) ApplyCatalog(ctx context.Context, manager *Manager) []V1Item {
	var failed []V1Item
	aliases := make([]string, 0, len(p.Aliases))
	for alias := range p.Aliases {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		if err := manager.PutAlias(ctx, alias, p.Aliases[alias]); err != nil {
			failed = append(failed, V1Item{Path: "modelAliases." + alias, Reason: "not accepted by the catalog (unknown or duplicate member, or name conflict); " + v1ComboNeedsBase})
		}
	}
	// A Combo may name another Combo, so a Combo that is refused only because the
	// one it names is not stored yet is tried again once the others are in.
	pending := append([]Combo(nil), p.Combos...)
	for len(pending) > 0 {
		var refused []Combo
		for _, combo := range pending {
			if _, err := manager.PutCombo(ctx, combo); err != nil {
				refused = append(refused, combo)
			}
		}
		if len(refused) == len(pending) {
			for _, combo := range refused {
				failed = append(failed, V1Item{Path: "combos." + combo.Name, Reason: "not accepted by the catalog (unknown or duplicate member, or name conflict); " + v1ComboNeedsBase})
			}
			break
		}
		pending = refused
	}
	return failed
}

func isEmptyJSON(raw json.RawMessage) bool {
	switch string(bytes.TrimSpace(raw)) {
	case "", "null", "{}", "[]":
		return true
	}
	return false
}
