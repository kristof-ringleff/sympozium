// Package cellninstall publishes an operator-reviewed native starter catalogue.
// It does not issue a run, read model keys, replace resources or start an owner.
package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/zeebo/blake3"
	"io"
	"os"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Options struct {
	Namespace, ConfigurationDir, OutputDir, StatePath string
	OwnerTarget, Scope                                string
	ControllerNamespace                               string
	PackageHash                                       string
}

type catalogue struct {
	SystemPrompt string                       `json:"systemPrompt"`
	Worker       api.AgentRuntimeCellnProfile `json:"worker"`
	// Toolbox is the runtime composed with every tool below, in this order
	// (a package built by a Celln that exports it); absent in older ones.
	Toolbox *api.AgentRuntimeCellnProfile `json:"toolbox,omitempty"`
	Tools   []struct {
		Name string            `json:"name"`
		Spec api.CellnToolSpec `json:"spec"`
	} `json:"tools"`
}

type receipt struct {
	APIVersion          string              `json:"apiVersion"`
	PackageHash         string              `json:"packageHash"`
	CatalogueHash       string              `json:"catalogueHash"`
	NativeTemplateHash  string              `json:"nativeTemplateHash"`
	Principal           string              `json:"principal"`
	ModelProfile        string              `json:"modelProfile"`
	Model               api.ModelSpec       `json:"model"`
	HostLimits          api.EnduringRunSpec `json:"hostLimits"`
	ExecutionAuthorized bool                `json:"executionAuthorized"`
	Readiness           string              `json:"readiness"`
}

func read(path string, output any) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return "", fmt.Errorf("bounded regular installation input required: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return "", fmt.Errorf("installation input exceeds bound")
	}
	return fmt.Sprintf("blake3:%x", blake3.Sum256(raw)), json.Unmarshal(raw, output)
}

// ControllerRolledOut reports whether the general controller's current
// generation is fully available, the precondition for writing native authority.
func ControllerRolledOut(ctx context.Context, store client.Client, namespace string) (bool, error) {
	var d appsv1.Deployment
	if err := store.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "sympozium-controller-manager"}, &d); err != nil {
		return false, err
	}
	replicas := d.Spec.Replicas
	return len(d.Spec.Template.Spec.Containers) == 1 && replicas != nil && *replicas >= 1 && d.Status.ObservedGeneration >= d.Generation && d.Status.Replicas == *replicas && d.Status.UpdatedReplicas == *replicas && d.Status.AvailableReplicas == *replicas, nil
}
