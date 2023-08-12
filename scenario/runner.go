package scenario

import (
	"context"
	"fmt"
	"log"
	"time"

	coreapis "github.com/aws/karpenter-core/pkg/apis"
	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter/pkg/apis"
	"github.com/samber/lo"
	"go.uber.org/multierr"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"knative.dev/pkg/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Runner struct {
	client *kubernetes.Clientset
	start  time.Time
	kubeClient client.Client
	variables map[string]int
}

func NewRunner(kubeConfig string) (*Runner, error) {
	// get our K8s client setup
	config, err := clientcmd.BuildConfigFromFlags("", kubeConfig)
	if err != nil {
		return nil, err
	}
	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client, %w", err)
	}
	c, err := client.New(config, client.Options{Scheme: Scheme()})
	if err != nil {
		return nil, fmt.Errorf("getting kubernetes client, %w", err)
	}

	return &Runner{
		client: clientSet,
		kubeClient: c,
		variables: map[string]int{},
	}, nil
}

func Scheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	// register karpenter-core CRDs
	lo.Must0(coreapis.AddToScheme(scheme))
	// register karpenter CRDs
	lo.Must0(apis.AddToScheme(scheme))
	return scheme
}

func (r *Runner) Execute(ctx context.Context, scen *Scenario) error {
	defer r.cleanup(scen)

	log.Println("creating deployments")
	for _, dep := range scen.Deployments {
		_, err := r.client.AppsV1().Deployments("default").Create(ctx, createDeployment(dep), metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating deployment %s, %w", dep.Name, err)
		}
	}
	log.Println("creating provisioners")
	for _, prov := range scen.Provisioners {
		p := createProvisioner(prov)
		if err := r.Apply(ctx, p); err != nil {
			return fmt.Errorf("creating provisioner %s, %w", prov.Name, err)
		}
	}

	cm, err := NewCostMonitor(ctx, r.client, scen.Name, scen.NodeSelector)
	if err != nil {
		return fmt.Errorf("creating cost monitor, %w", err)
	}
	defer cm.Stop()

	r.start = time.Now()
	r.log("starting scenario")
	for i := range scen.Events {
		select {
		case <-time.After(scen.timeDuration):
		case <-ctx.Done():
			r.log("interrupted, exiting")
		default:
			ev := scen.Events[i]
			if ev.ProvisionerEvent != nil || ev.DeploymentEvent != nil {
				go func() {
					select {
					case <-time.After(*ev.durationTime):
					case <-ctx.Done():
						return
					}
					r.execute(ctx, ev)
				}()
				continue
			}
			startMonitoring, err := r.executeWait(ctx, *ev.WaitEvent)
			if err != nil {
				return fmt.Errorf("failed wait, %w", err)
			}
			if startMonitoring {
				r.log("starting cost monitoring after %s", time.Since(r.start))
				r.start = time.Now()
				cm.Start(ctx, r.start)
			}
		}
	}
	return nil
}

func (r *Runner) executeWait(ctx context.Context, ev WaitEvent) (bool, error) {
	var valToCompare int
	if ev.Value.Integer != nil {
		valToCompare = *ev.Value.Integer
	} else if ev.Value.Variable != nil {
		val, ok := r.variables[*ev.Value.Variable]
		if !ok {
			return false, fmt.Errorf("getting variable for comparison in wait")
		}
		valToCompare = val
	}

	r.log("starting wait %s", ev.String())
	var objectCount int
	filterOpts := metav1.ListOptions{}
	if ev.Selector != nil {
		filterOpts.LabelSelector = *ev.Selector
	}
	// Max out at 6 hours for a wait. (3600 * 6 / 30 = 720)
	for i := 0; i < 720; i++ {
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("interrupted, exiting wait")
		default:
			if ev.ObjectType == "node" {
				if ev.Selector == nil {
					filterOpts.LabelSelector = "karpenter.sh/provisioner-name"
				}
				nodeList, err := r.client.CoreV1().Nodes().List(ctx, filterOpts)
				if err != nil {
					if i == 2159 {
						return false, fmt.Errorf("exiting wait, could not get nodes, %w", err)
					}
					r.log("failed getting nodes, %w", err)
				}
				objectCount = len(nodeList.Items)
			} else if ev.ObjectType == "pod" {
				podList, err := r.client.CoreV1().Pods("").List(ctx, filterOpts)
				if err != nil {
					if i == 2159 {
						return false, fmt.Errorf("exiting wait, could not get pods, %w", err)
					}
					r.log("failed getting pods %w", err)
				}
				objectCount = len(podList.Items)
			}
			if valToCompare == objectCount {
				r.log("Wait condition achieved, continuing with events after iteration %d", i)
				return ptr.BoolValue(ev.StartMonitoring), nil
			}
			r.log(fmt.Sprintf("Observed count was %d, Target was %d, Sleeping for 30s (%dth iteration)", objectCount, valToCompare, i))
			time.Sleep(30 * time.Second)
		}
	}
	return false, fmt.Errorf("failed to achieve wait condition, %s", ev.String())
}

func createDeployment(dep Deployment) *appsv1.Deployment {
	replicas := int32(0)
	ret := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      dep.Name,
			Labels: map[string]string{
				"ccmon": "owned",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": dep.Name,
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      dep.Name,
					Labels: map[string]string{
						"ccmon": "owned",
						"app":   dep.Name,
					},
				},
				Spec: v1.PodSpec{
					Affinity: dep.Affinity,
					Containers: []v1.Container{
						{
							Name:  "container",
							Image: "public.ecr.aws/eks-distro/kubernetes/pause:3.2",
							Resources: v1.ResourceRequirements{
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    dep.CPU.Quantity,
									v1.ResourceMemory: dep.Memory.Quantity,
								},
							},
						},
					},
				},
			},
		},
	}
	return ret
}
func createProvisioner(prov Provisioner) *v1alpha5.Provisioner {
	resources := v1.ResourceList{}
	if prov.CPULimits != nil {
		resources[v1.ResourceCPU] = resource.MustParse(*prov.CPULimits)
	}
	if prov.MemoryLimits != nil {
		resources[v1.ResourceMemory] = resource.MustParse(*prov.MemoryLimits)
	}
	ret := &v1alpha5.Provisioner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prov.Name,
		},
		Spec: v1alpha5.ProvisionerSpec{
			Requirements: prov.Requirements,
			Labels: prov.Labels,
			Taints: prov.Taints,
			Consolidation: &v1alpha5.Consolidation{
				Enabled: prov.ConsolidationEnabled,
			},
			TTLSecondsUntilExpired: prov.TTLSecondsUntilExpired,
			Limits: &v1alpha5.Limits{
				Resources: resources,
			},
		},
	}
	return ret
}

func (r *Runner) execute(ctx context.Context, ev Event) error {
	if ev.DeploymentEvent != nil {
		return r.executeDeploymentEvent(ctx, *ev.DeploymentEvent)
	} else if ev.ProvisionerEvent != nil {
		return r.executeProvisionerEvent(ctx, *ev.ProvisionerEvent)
	}
	return nil
}
func (r *Runner) executeDeploymentEvent(ctx context.Context, ev DeploymentEvent) error {
	r.log("scaling %s to %d replicas", ev.Deployment, ev.Replicas)

	for try := 0; try < 250; try++ {
		s, err := r.client.AppsV1().Deployments("default").GetScale(ctx, ev.deployment.Name, metav1.GetOptions{})
		if err != nil {
			r.log("unable to get scale for %s, %s", ev.Deployment, err)
		}

		s.Spec.Replicas = int32(ev.Replicas)
		scale, err := r.client.AppsV1().Deployments("default").UpdateScale(context.Background(),
			ev.deployment.Name, s, metav1.UpdateOptions{})
		if err == nil {
			r.log("successfully scaled to %d", scale.Spec.Replicas)
			break
		}
		if err != nil {
			if try == 249 {
				return err
			}
			r.log("unable to scale %s, %s", ev.Deployment, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil
}

func (r *Runner) executeProvisionerEvent(ctx context.Context, ev ProvisionerEvent) error {
	r.log("updating provisioner %s", ev.Name)
	provisioner := createProvisioner(*ev.provisioner)

	if ev.CPULimits != nil {
		provisioner.Spec.Limits.Resources[v1.ResourceCPU] = resource.MustParse(*ev.CPULimits)
	}
	if ev.MemoryLimits != nil {
		provisioner.Spec.Limits.Resources[v1.ResourceMemory] = resource.MustParse(*ev.MemoryLimits)
	}
	if ev.NodeRequirements != nil {
		provisioner.Spec.Requirements = ev.NodeRequirements
	}

	for try := 0; try < 100; try++ {
		err := r.Apply(ctx, provisioner)
		if err != nil {
			r.log("applying provisioner %s, %w", provisioner.Name, err)
		}
		if err == nil {
			break
		}
		if err != nil {
			if try == 99 {
				return err
			}
			r.log("unable to apply provisioner %s, %s", ev.provisioner, err)
		}
	}
	return nil
}

func (r *Runner) log(s string, args ...interface{}) {
	line := fmt.Sprintf(s, args...)
	log.Printf("[%s] %s", time.Since(r.start), line)
}

func (r *Runner) cleanup(scen *Scenario) {
	ctx := context.Background()
	gracePeriod := int64(0)
	for _, dep := range scen.Deployments {
		err := r.client.AppsV1().Deployments("default").Delete(ctx, dep.Name,
			metav1.DeleteOptions{
				TypeMeta:           metav1.TypeMeta{},
				GracePeriodSeconds: &gracePeriod,
			})
		if err != nil {
			r.log("deleting deployment %s (%s), %w", dep.Name, dep.Name, err)
		}
	}
	for _, prov := range scen.Provisioners {
		p := createProvisioner(prov)
		provisioner := &v1alpha5.Provisioner{}

		if err := r.kubeClient.Get(ctx, client.ObjectKeyFromObject(p), provisioner); err != nil {
			r.log("getting provisioner for cleanup, %w", err)
			continue
		}

		err := r.kubeClient.Delete(ctx, provisioner, &client.DeleteOptions{
			GracePeriodSeconds: &gracePeriod,
		})
		if err != nil {
			r.log("deleting provisioner %s, %w", p.Name, err)
		}
	}
}


func (r *Runner) Apply(ctx context.Context, provisioners ...*v1alpha5.Provisioner) error {
	var multiErr error
	for _, prov := range provisioners {
		current := prov.DeepCopy()
		// Create or Update
		if err := r.kubeClient.Get(ctx, client.ObjectKeyFromObject(prov), current); err != nil {
			if errors.IsNotFound(err) {
				if err := r.kubeClient.Create(ctx, prov); err != nil {
					multiErr = multierr.Append(multiErr, fmt.Errorf("creating object, %w", err))
				}
			} else {
				multiErr = multierr.Append(multiErr, fmt.Errorf("getting object, %w", err))
			}
		} else {
			prov.SetResourceVersion(current.GetResourceVersion())
			if err := r.kubeClient.Update(ctx, prov); err != nil {
				multiErr = multierr.Append(multiErr, fmt.Errorf("updating object, %w", err))
			}
		}
	}
	return multiErr
}
