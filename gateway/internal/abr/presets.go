// Package abr holds the ABR preset catalog the gateway needs to mint
// output-URL bundles for the abr-runner.
//
// The runner expects presigned PUT URLs per rendition (one for the .m3u8
// playlist and one for the muxed stream file) plus one for the master
// manifest. The set of rendition names is preset-defined, so the gateway
// reads the same presets.yaml the runner uses and exposes a lookup.
//
// The YAML is embedded at build time so the gateway is self-contained;
// re-vendor with `make sync-presets` if the runner ships new presets.
package abr

import (
	_ "embed"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

//go:embed presets.yaml
var presetsYAML []byte

// Rendition is the minimal projection the gateway cares about — names
// drive the output_urls map keys.
type Rendition struct {
	Name string `yaml:"name"`
}

// Preset is the minimal projection the gateway cares about.
type Preset struct {
	Name       string      `yaml:"name"`
	Renditions []Rendition `yaml:"renditions"`
}

type presetFile struct {
	Presets []Preset `yaml:"presets"`
}

var (
	loaded  map[string]Preset
	loadErr error
)

func init() {
	var pf presetFile
	if err := yaml.Unmarshal(presetsYAML, &pf); err != nil {
		loadErr = fmt.Errorf("abr: parse embedded presets.yaml: %w", err)
		return
	}
	loaded = make(map[string]Preset, len(pf.Presets))
	for _, p := range pf.Presets {
		loaded[p.Name] = p
	}
}

// Get returns the preset by name. Returns (_, false) when the runner
// doesn't ship a preset by that name.
func Get(name string) (Preset, bool) {
	if loadErr != nil {
		return Preset{}, false
	}
	p, ok := loaded[name]
	return p, ok
}

// Names returns the catalog of preset names known to the gateway.
func Names() []string {
	out := make([]string, 0, len(loaded))
	for n := range loaded {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// RenditionNames returns the ordered list of rendition names for a preset,
// suitable for keying into output_urls.renditions.
func (p Preset) RenditionNames() []string {
	out := make([]string, len(p.Renditions))
	for i, r := range p.Renditions {
		out[i] = r.Name
	}
	return out
}
