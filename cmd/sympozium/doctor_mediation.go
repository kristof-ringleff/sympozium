package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
)

// The CA key was discarded, so certificates are renewed by rotating.
const mediationRotateRemedy = "delete the five mediation objects (docs/guides/celln-mediated-model-access.md, Rotation), run sympozium install, then restart the controller, model gateway and celln-node pods"

// mediationObjectNames are the bootstrap objects' names: the chart's
// defaults, or what the release's celln.mediation values name instead.
func mediationObjectNames(rel *releaseInfo) (controller, gateway, node, trust string) {
	controller, gateway, node, trust = cellninstall.MediationControllerSecret, cellninstall.MediationGatewaySecret, cellninstall.MediationNodeSecret, cellninstall.MediationTrustConfigMap
	if rel == nil {
		return
	}
	celln, _ := rel.Config["celln"].(map[string]interface{})
	mediation, _ := celln["mediation"].(map[string]interface{})
	for key, target := range map[string]*string{"controllerSecret": &controller, "gatewaySecret": &gateway, "nodeSecret": &node, "trustConfigMap": &trust} {
		if name, _ := mediation[key].(string); name != "" {
			*target = name
		}
	}
	return
}

// checkMediation reports whether Agents can use their own provider key on
// the fleet: the chart recorded the gateway in celln-system, the bootstrapped
// trust is present and the gateway is ready. There is no route list to
// check; the Agent's connection is the route.
func (d *doctor) checkMediation(ctx context.Context, rel *releaseInfo) doctorFinding {
	const check = "Mediated model access"
	if installed, err := d.fleetInstalled(ctx); err != nil {
		return failed(check, err)
	} else if !installed {
		return doctorFinding{Check: check, Status: statusPass, Summary: "no Celln fleet is installed; mediated model access runs on a fleet"}
	}
	record, err := cellninstall.ReadMediationRecord(ctx, d.client)
	if err != nil {
		return failed(check, err)
	}
	if !record.Enabled {
		return doctorFinding{Check: check, Status: statusFail, Summary: "the release records no model gateway (ConfigMap celln-system/celln-mediation): Celln Agents have no model path", Remedy: []string{"sympozium install   # wires the model gateway and bootstraps its trust"}}
	}
	f := doctorFinding{Check: check, Status: statusPass}
	raise := func(status string) {
		if f.Status != statusFail {
			f.Status = status
		}
	}
	controller, gateway, node, trust := mediationObjectNames(rel)
	gatewaySecret, nodeSecret, trustConfigMap := &corev1.Secret{}, &corev1.Secret{}, &corev1.ConfigMap{}
	for _, object := range []struct {
		kind, namespace, name string
		into                  client.Object
	}{
		{"Secret", helmNamespace, controller, &corev1.Secret{}},
		{"Secret", helmNamespace, gateway, gatewaySecret},
		{"Secret", cellnSystemNamespace, node, nodeSecret},
		{"ConfigMap", helmNamespace, trust, trustConfigMap},
		{"ConfigMap", cellnSystemNamespace, trust, &corev1.ConfigMap{}},
	} {
		if err := d.client.Get(ctx, types.NamespacedName{Namespace: object.namespace, Name: object.name}, object.into); apierrors.IsNotFound(err) {
			f.Details = append(f.Details, doctorDetail{Object: fmt.Sprintf("%s %s/%s", object.kind, object.namespace, object.name), Problem: "missing; the controller, dispatchers and gateway cannot start without it"})
			raise(statusFail)
		} else if err != nil {
			return failed(check, err)
		}
	}
	if f.Status == statusFail {
		f.Remedy = append(f.Remedy, "sympozium install   # bootstraps the mediation trust and derives its values; verifies existing objects and never replaces one")
	} else if expiry, ok := cellninstall.MediationCertificateExpiry([]byte(trustConfigMap.Data["ca.crt"]), gatewaySecret.Data["tls.crt"], nodeSecret.Data["tls.crt"]); ok {
		switch left := expiry.Sub(d.now()); {
		case left <= 0:
			f.Details = append(f.Details, doctorDetail{Object: fmt.Sprintf("ConfigMap %s/%s", helmNamespace, trust), Problem: fmt.Sprintf("the mediation CA or a server certificate expired %s; the controller, receiver and gateway refuse each other", expiry.UTC().Format(time.DateOnly)), Remedy: mediationRotateRemedy})
			raise(statusFail)
		case left <= cellninstall.MediationExpiryWarning:
			raise(statusWarn)
			f.Details = append(f.Details, doctorDetail{Object: fmt.Sprintf("ConfigMap %s/%s", helmNamespace, trust), Problem: fmt.Sprintf("the mediation certificates expire %s (in %d days); rotate them before then", expiry.UTC().Format(time.DateOnly), int(left.Hours()/24)), Remedy: mediationRotateRemedy})
		}
	}
	var deployments appsv1.DeploymentList
	if err := d.client.List(ctx, &deployments, client.InNamespace(helmNamespace)); err != nil {
		return failed(check, err)
	}
	index := slices.IndexFunc(deployments.Items, func(dep appsv1.Deployment) bool { return strings.HasSuffix(dep.Name, "-model-gateway") })
	switch {
	case index < 0:
		f.Details = append(f.Details, doctorDetail{Object: "Deployment " + helmNamespace + "/<release>-model-gateway", Problem: "missing; the release enables mediation but deploys no model gateway", Remedy: "sympozium install   # or helm upgrade with the mediation values"})
		raise(statusFail)
	case deployments.Items[index].Status.AvailableReplicas < 1 || deployments.Items[index].Status.ObservedGeneration < deployments.Items[index].Generation:
		name := deployments.Items[index].Name
		f.Details = append(f.Details, doctorDetail{Object: "Deployment " + helmNamespace + "/" + name, Problem: "not ready; ready means PostgreSQL, RBAC and keys were accepted", Remedy: fmt.Sprintf("kubectl -n %s logs deploy/%s", helmNamespace, name)})
		raise(statusFail)
	}
	// The Agent's connection is the route; what the operator may have placed
	// on it is the policy's origin guardrail, reported so a refusal there is
	// no surprise.
	guardrail := ""
	if facts, err := cellninstall.ReadFleetFacts(ctx, d.client); err == nil {
		_, policyName, _ := cellninstall.PlatformCatalogueNames(facts.Scope)
		var policy sympoziumv1alpha1.CellnExecutionPolicy
		if err := d.client.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return failed(check, err)
		}
		switch {
		case len(policy.Spec.AllowedOrigins) != 0 && len(policy.Spec.DeniedOrigins) != 0:
			guardrail = fmt.Sprintf("; policy %s allows keys to go only to %s and never to %s", policyName, strings.Join(policy.Spec.AllowedOrigins, ", "), strings.Join(policy.Spec.DeniedOrigins, ", "))
		case len(policy.Spec.AllowedOrigins) != 0:
			guardrail = fmt.Sprintf("; policy %s allows keys to go only to %s", policyName, strings.Join(policy.Spec.AllowedOrigins, ", "))
		case len(policy.Spec.DeniedOrigins) != 0:
			guardrail = fmt.Sprintf("; policy %s never lets a key go to %s", policyName, strings.Join(policy.Spec.DeniedOrigins, ", "))
		case policy.Name != "":
			guardrail = fmt.Sprintf("; policy %s lets an Agent's own key go to any origin the gateway reaches (narrow it with allowedOrigins/deniedOrigins)", policyName)
		}
	}
	if f.Status == statusFail {
		f.Summary = fmt.Sprintf("enabled, but %d of its parts are missing or not ready", len(f.Details))
	} else {
		f.Summary = "enabled; trust objects present and the model gateway is ready: every Agent brings its own connection and key, pinned to that endpoint's origin" + guardrail
	}
	return f
}
