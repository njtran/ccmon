package scenario

import (
	"fmt"

	"github.com/samber/lo"
	"go.uber.org/multierr"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"knative.dev/pkg/ptr"
)

func (s *Scenario) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("scenario has no name")
	}
	if s.Duration == "" {
		return fmt.Errorf("scenario has no duration")
	}
	if len(s.Events) == 0 {
		return fmt.Errorf("scenario has no events")
	}

	if s.NodeSelector != nil {
		_, err := metav1.ParseToLabelSelector(*s.NodeSelector)
		if err != nil {
			return fmt.Errorf("invalid node selector %q, %w", *s.NodeSelector, err)
		}
	}

	uniqueDeps := map[string]*Deployment{}
	for i := range s.Deployments {
		dep := s.Deployments[i]
		uniqueDeps[dep.Name] = &dep
	}
	if len(uniqueDeps) != len(s.Deployments) {
		return fmt.Errorf("duplicate deployment names")
	}
	uniqueProvisioners := map[string]*Provisioner{}
	for i := range s.Provisioners {
		prov := s.Provisioners[i]
		uniqueProvisioners[prov.Name] = &prov
	}
	if len(uniqueProvisioners) != len(s.Provisioners) {
		return fmt.Errorf("duplicate provisioner names")
	}
	var multiErr error
	singleStartMonitoring := false
	for i := range s.Events {
		ev := &s.Events[i]
		if err := ev.Validate(); err != nil {
			multiErr = multierr.Append(multiErr, err)
		}
		if ev.WaitEvent != nil {
			if ptr.BoolValue(ev.WaitEvent.StartMonitoring) {
				if singleStartMonitoring {
					return fmt.Errorf("only one wait event can start monitoring")
				}
				singleStartMonitoring = ptr.BoolValue(ev.WaitEvent.StartMonitoring)
			}
		}
		if ev.DeploymentEvent != nil {
			if ev.durationTime == nil {
				return fmt.Errorf("deployment event needs to have a time, %s", ev.DeploymentEvent.Deployment)
			}
			dep, ok := uniqueDeps[ev.DeploymentEvent.Deployment]
			if !ok {
				return fmt.Errorf("unknown deployment %s", ev.DeploymentEvent.Deployment)
			}
			ev.DeploymentEvent.deployment = dep
		}
		if ev.ProvisionerEvent != nil {
			if ev.durationTime == nil {
				return fmt.Errorf("provisioner event needs to have a time, %s", ev.ProvisionerEvent.Name)
			}
			prov, ok := uniqueProvisioners[ev.ProvisionerEvent.Name]
			if !ok {
				return fmt.Errorf("unknown provisioner %s", ev.ProvisionerEvent.Name)
			}
			ev.ProvisionerEvent.provisioner = prov
		}
	}
	if multiErr != nil {
		return fmt.Errorf("validation failed, %w", multiErr)
	}

	// // do the events repeat?
	// if s.RepeatAfter != nil {
	// 	var newEvents []Event
	// 	var nextStart time.Duration
	// 	for i := range s.Events {
	// 		ev := s.Events[i]
	// 		if *ev.Time > nextStart {
	// 			nextStart = *ev.Time
	// 		}
	// 	}
	// 	nextStart += *s.RepeatAfter

	// 	for nextStart < s.Duration {
	// 		for i := range s.Events {
	// 			cp := s.Events[i]
	// 			nextStart += *cp.Time
	// 			cp.Time = &nextStart
	// 			newEvents = append(newEvents, cp)
	// 		}
	// 		nextStart += *s.RepeatAfter
	// 	}
	// 	s.Events = append(s.Events, newEvents...)
	// }
	// ensure events are sorted by time
	// sort.SliceStable(s.Events, func(i, j int) bool {
	// 	return *s.Events[i].Time < *s.Events[j].Time
	// })
	return nil
}


func (e *Event) Validate() error {
	// First validate that the event is only one type
	deploy := lo.Ternary(e.DeploymentEvent == nil, 0, 1)
	prov := lo.Ternary(e.ProvisionerEvent == nil, 0, 1)
	wait := lo.Ternary(e.WaitEvent == nil, 0, 1)
	if sum := deploy + prov + wait; sum != 1 {
		return fmt.Errorf("event must be one type, got %d", sum)
	}
	if e.DeploymentEvent != nil {
		if e.DeploymentEvent.Replicas < 0 {
			return fmt.Errorf("replicas must be non-negative")
		}
	}
	if e.ProvisionerEvent != nil {
		if e.ProvisionerEvent.CPULimits != nil {
			if _, err := resource.ParseQuantity(*e.ProvisionerEvent.CPULimits); err != nil {
				return fmt.Errorf("cpu limits are not parseable")
			}
		}
		if e.ProvisionerEvent.MemoryLimits != nil {
			if _, err := resource.ParseQuantity(*e.ProvisionerEvent.MemoryLimits); err != nil {
				return fmt.Errorf("memory limits are not parseable")
			}
		}
	}
	if e.WaitEvent != nil {
		if e.durationTime != nil {
			return fmt.Errorf("cannot set time with wait event")
		}
		if e.WaitEvent.ObjectType != "node" && e.WaitEvent.ObjectType != "pod" {
			return fmt.Errorf("object type must be either node or pod")
		}
		value := e.WaitEvent.Value
		allowedOperators := sets.NewString("lt", "gt", "eq", "")
		if !allowedOperators.Has(value.Operator) {
			return fmt.Errorf("operator must be lt, gt, eq, or empty(=)")
		}
		if value.Integer != nil {
			if *value.Integer < 0 {
				return fmt.Errorf("value integer must be non-negative")
			}
		}
	}
	return nil
}
