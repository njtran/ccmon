package scenario

import (
	"bytes"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/util/yaml"
	"knative.dev/pkg/ptr"
)

type Scenario struct {
	Name         string          `yaml:"name"`
	Duration     string          `yaml:"duration"`
	Deployments  []Deployment    `yaml:"deployments"`
	Provisioners []Provisioner   `yaml:"provisioners"`
	RepeatAfter  *string         `yaml:"repeatAfter"`
	Events       []Event         `yaml:"events"`
	NodeSelector *string         `yaml:"nodeSelector"`
	timeDuration time.Duration
	timeRepeatAfter *time.Duration
}
type Deployment struct {
	Name         string       `yaml:"name"`
	CPU          Quantity     `yaml:"cpu"`
	Memory       Quantity     `yaml:"memory"`
	Affinity     *v1.Affinity `yaml:"affinity"`
}

type Provisioner struct {
	Name                   string                       `yaml:"name"`
	Requirements           []v1.NodeSelectorRequirement `yaml:"requirements"`
	Labels                 map[string]string            `yaml:"labels"`
	Taints                 []v1.Taint                   `yaml:"taints"`
	CPULimits              *string            		    `yaml:"cpuLimits"`
	MemoryLimits		   *string                      `yaml:"memoryLimits:`
	ConsolidationEnabled   *bool                        `yaml:"consolidationEnabled"`
	TTLSecondsUntilExpired *int64                       `yaml:"ttlSecondsUntilExpired"`
}

type Event struct {
	Time             *string           `yaml:"time"`
	durationTime     *time.Duration
	DeploymentEvent  *DeploymentEvent  `yaml:"deploymentEvent"`
	ProvisionerEvent *ProvisionerEvent `yaml:"provisionerEvent"`
	WaitEvent        *WaitEvent        `yaml:"waitEvent"`
}

type DeploymentEvent struct {
	Deployment string        `yaml:"deployment"`
	Replicas   int           `yaml:"replicas"`
	deployment *Deployment
}

type ProvisionerEvent struct {
	Name             string                       `yaml:"name"`
	NodeRequirements []v1.NodeSelectorRequirement `yaml:"requirements"`
	CPULimits        *string                      `yaml:"cpuLimits"`
	MemoryLimits     *string					  `yaml:"memoryLimits"`
	provisioner      *Provisioner
}

type WaitEvent struct {
	Value           Value   `yaml:"value"`
	Selector        *string `yaml:"selector"`
	WriteTo         *string `yaml:"writeTo"`
	ObjectType      string  `yaml:"objectType"`
	StartMonitoring *bool    `yaml:"startMonitoring"`
}

type Value struct {
	Integer   *int    `yaml:"integer"`
	Variable  *string `yaml:"variable"`
	Operator  string  `yaml:"operator"`
}

func Open(r io.Reader) (*Scenario, error) {
	decoder := yaml.NewYAMLToJSONDecoder(r)
	var s Scenario
	if err := decoder.Decode(&s); err != nil {
		return nil, fmt.Errorf("decoding scenario, %w", err)
	}
	// Fill Duration Fields
	if err := s.fillDurations(); err != nil {
		return nil, fmt.Errorf("could not parse durations, %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("validating scenario, %w", err)
	}
	return &s, nil
}

func (s *Scenario) fillDurations() error {
	if s.Duration != "" {
		d, err := time.ParseDuration(s.Duration)
		if err != nil {
			return fmt.Errorf("couldn't parse duration for scenario.Duration, %s", s.Duration)
		}
		s.timeDuration = d
	}
	if s.RepeatAfter != nil {
		d, err := time.ParseDuration(*s.RepeatAfter)
		if err != nil {
			return fmt.Errorf("couldn't parse duration for scenario.RepeatAfter, %s", s.RepeatAfter)
		}
		s.timeRepeatAfter = &d
	}
	for i, e := range s.Events {
		if e.Time != nil {
			d, err := time.ParseDuration(*e.Time)
			if err != nil {
				return fmt.Errorf("couldn't parse duration for event.Time, %s", s.RepeatAfter)
			}
			s.Events[i].durationTime = &d
		}
	}
	return nil
}

func (s *Scenario) String() string {
	var b bytes.Buffer
	tw := tabwriter.NewWriter(&b, 4, 2, 1, ' ', 0)
	fmt.Fprintf(tw, "Name:\t%s\n", s.Name)
	fmt.Fprintf(tw, "Duration:\t%s\n", s.timeDuration)

	for _, dep := range s.Deployments {
		fmt.Fprintf(tw, "Deployment:\t%s\n", dep.Name)
		fmt.Fprintf(tw, "\t - CPU:\t%s\n", dep.CPU)
		fmt.Fprintf(tw, "\t - Memory:\t%s\n", dep.Memory)
		// fmt.Fprintf(tw, "\t - Affinity:\t%s\n", *dep.Affinity)
	}
	for _, prov := range s.Provisioners {
		fmt.Fprint(tw, prov.String())
	}

	fmt.Fprintf(tw, "Events\n")
	for _, ev := range s.Events {
		fmt.Fprint(tw, ev.String())
	}
	tw.Flush()
	return b.String()
}

func (ev *Event) String() string {
	if ev.DeploymentEvent != nil {
		return fmt.Sprintf(" - %s => scale %s to %d\n", ev.durationTime, ev.DeploymentEvent.Deployment, ev.DeploymentEvent.Replicas)
	}
	if ev.ProvisionerEvent != nil {
		return fmt.Sprintf(" - %s => %s", ev.durationTime, ev.ProvisionerEvent.String())
	}
	if ev.WaitEvent != nil {
		return ev.WaitEvent.String()
	}
	return ""
}

func (p Provisioner) String() string {
	var b bytes.Buffer
	tw := tabwriter.NewWriter(&b, 4, 2, 1, ' ', 0)

	fmt.Fprintf(tw, "Name:\t%s\n", p.Name)
	fmt.Fprintf(tw, "\tLabels:\t%s\n", p.Labels)
	fmt.Fprintf(tw, "\tTaints:\t%s\n", p.Taints)
	fmt.Fprintf(tw, "\tRequirements:\t%s\n", p.Requirements)
	fmt.Fprintf(tw, "\tCPULimits:\t%s\n", ptr.StringValue(p.CPULimits))
	fmt.Fprintf(tw, "\tMemoryLimits:\t%s\n", ptr.StringValue(p.MemoryLimits))
	fmt.Fprintf(tw, "\tConsolidationEnabled:\t%t\n", ptr.BoolValue(p.ConsolidationEnabled))
	fmt.Fprintf(tw, "\tTTLSecondsUntilExpired:\t%d\n", ptr.Int64Value(p.TTLSecondsUntilExpired))

	tw.Flush()
	return b.String()
}

func (p *ProvisionerEvent) String() string {
	var b bytes.Buffer
	tw := tabwriter.NewWriter(&b, 4, 2, 1, ' ', 0)

	fmt.Fprintf(tw, "changing provisioner:\t")
	if p.NodeRequirements != nil {
		fmt.Fprintf(tw, "requirements: %s, ", p.NodeRequirements)
	}
	if p.CPULimits != nil {
		fmt.Fprintf(tw, "cpuLimits: %s, ", *p.CPULimits)
	}
	if p.MemoryLimits != nil {
		fmt.Fprintf(tw, "memoryLimits: %s, ", *p.MemoryLimits)
	}

	tw.Flush()
	return b.String()
}

func (w *WaitEvent) String() string {
	// If we're writing to a variable, include that.
	writeTo := lo.Ternary(w.WriteTo != nil, fmt.Sprintf(", will write result to scenario variable %s", ptr.StringValue(w.WriteTo)), "")

	selectorString := lo.Ternary(w.Selector != nil, fmt.Sprintf(" with selector %s", ptr.StringValue(w.Selector)), "")

	startMonitoring := lo.Ternary(w.StartMonitoring != nil, "-- will start monitoring after", "")
	return fmt.Sprintf(" - => waiting for count of %s%s to be %s%s\t\t%s\n", w.ObjectType + "(s)", selectorString,
		 w.Value.String(), writeTo, startMonitoring)
}

func (v *Value) String() string {
	op := ""
	switch v.Operator {
	case "lt":
		op = "<"
	case "gt":
		op = ">"
	default:
		op = "="
	}
	if v.Integer != nil {
		return fmt.Sprintf("%s%d", op, *v.Integer)
	}
	if v.Variable != nil {
		return fmt.Sprintf("%s%s", op, *v.Variable)
	}
	return ""
}
