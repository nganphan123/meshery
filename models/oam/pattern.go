package oam

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/layer5io/meshery/models/oam/core/v1alpha1"
	cytoscapejs "gonum.org/v1/gonum/graph/formats/cytoscapejs"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Pattern is the golang representation of the Pattern
// config file model
type Pattern struct {
	Name     string              `yaml:"name,omitempty"`
	Services map[string]*Service `yaml:"services,omitempty"`
}

// Service represents the services defined within the appfile
type Service struct {
	Type      string   `yaml:"type,omitempty"`
	Namespace string   `yaml:"namespace,omitempty"`
	DependsOn []string `yaml:"dependsOn,omitempty"`

	Settings map[string]interface{} `yaml:"settings,omitempty"`
	Traits   map[string]interface{} `yaml:"traits,omitempty"`
}

// NewPatternFile takes in raw yaml and encodes it into a construct
func NewPatternFile(yml []byte) (af Pattern, err error) {
	err = yaml.Unmarshal(yml, &af)

	for _, svc := range af.Services {
		svc.Settings = RecursiveCastMapStringInterfaceToMapStringInterface(svc.Settings)
		svc.Traits = RecursiveCastMapStringInterfaceToMapStringInterface(svc.Traits)

		if svc.Settings == nil {
			svc.Settings = map[string]interface{}{}
		}
		if svc.Traits == nil {
			svc.Traits = map[string]interface{}{}
		}

		fmt.Printf("%+#v\n\n", svc)
	}

	return
}

// GetApplicationComponent generates OAM Application Components from the
// the given Pattern file
func (p *Pattern) GetApplicationComponent(name string) (v1alpha1.Component, error) {
	svc, ok := p.Services[name]
	if !ok {
		return v1alpha1.Component{}, fmt.Errorf("invalid service name")
	}

	comp := v1alpha1.Component{
		TypeMeta:   v1.TypeMeta{Kind: "Component", APIVersion: "core.oam.dev/v1alpha2"},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: svc.Namespace},
		Spec: v1alpha1.ComponentSpec{
			Type:     svc.Type,
			Settings: svc.Settings,
		},
	}

	return comp, nil
}

// GenerateApplicationConfiguration generates OAM Application Configuration from the
// the given Pattern file for a particular deploymnet
func (p *Pattern) GenerateApplicationConfiguration() (v1alpha1.Configuration, error) {
	config := v1alpha1.Configuration{
		TypeMeta:   v1.TypeMeta{Kind: "ApplicationConfiguration", APIVersion: "core.oam.dev/v1alpha2"},
		ObjectMeta: v1.ObjectMeta{Name: p.Name},
	}

	// Create configs for each component
	for k, v := range p.Services {
		// Indicates that map for properties is not empty
		if len(v.Traits) > 0 {
			specComp := v1alpha1.ConfigurationSpecComponent{
				ComponentName: k,
			}

			for k2, v2 := range v.Traits {
				castToMap, ok := v2.(map[string]interface{})

				trait := v1alpha1.ConfigurationSpecComponentTrait{
					Name: k2,
				}

				if !ok {
					castToMap = map[string]interface{}{}
				}

				trait.Properties = castToMap

				specComp.Traits = append(specComp.Traits, trait)
			}

			config.Spec.Components = append(config.Spec.Components, specComp)
		}
	}

	return config, nil
}

// GetServiceType returns the type of the service
func (p *Pattern) GetServiceType(name string) string {
	return p.Services[name].Type
}

// ToCytoscapeJS converts pattern file into cytoscape object
func (p *Pattern) ToCytoscapeJS() (cytoscapejs.GraphElem, error) {
	var cy cytoscapejs.GraphElem

	// Not specifying any cytoscapejs layout
	// should fallback to "default" layout

	// Not specifying styles, may get applied on the
	// client side

	// Set up the nodes
	for name, svc := range p.Services {
		// Skip if type is either prometheus or grafana
		if !notIn(svc.Type, []string{"prometheus", "grafana"}) {
			continue
		}

		rand.Seed(time.Now().UnixNano())

		elemData := cytoscapejs.ElemData{
			ID: name, // Assuming that the service names are unique
		}

		elemPosition := cytoscapejs.Position{
			X: float64(rand.Intn(100)),
			Y: float64(rand.Intn(100)),
		}

		elem := cytoscapejs.Element{
			Data:       elemData,
			Position:   &elemPosition,
			Selectable: true,
			Grabbable:  true,
			Scratch: map[string]Service{
				"_data": *svc,
			},
		}

		cy.Elements = append(cy.Elements, elem)
	}

	return cy, nil
}

func notIn(name string, prohibited []string) bool {
	for _, p := range prohibited {
		if strings.HasPrefix(strings.ToLower(name), p) {
			return false
		}
	}

	return true
}
