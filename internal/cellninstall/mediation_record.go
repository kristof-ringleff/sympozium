package cellninstall

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The chart records that the release wires the model gateway, beside the
// fleet's other facts in celln-system. The installer, the API server and
// `sympozium doctor` read it. It names no provider, model or origin: the
// Agent's connection is the route, and the operator narrows it with
// allowedOrigins/deniedOrigins on the scope's CellnExecutionPolicy.

const (
	// MediationRecordConfigMap is the record; its presence means the release
	// mediates model access. (Releases before 0.12 recorded their declared
	// routes under celln-mediated-routes; that object is left behind and
	// ignored.)
	MediationRecordConfigMap = "celln-mediation"
	// legacyMediationRecordConfigMap is read as "enabled" so an upgrade from
	// 0.11 is recognised before its first new render.
	legacyMediationRecordConfigMap = "celln-mediated-routes"
)

// MediationRecord says whether the release mediates model access.
type MediationRecord struct {
	Enabled bool
}

// ReadMediationRecord reads the record. A cluster without one does not run
// the gateway.
func ReadMediationRecord(ctx context.Context, store client.Reader) (MediationRecord, error) {
	for _, name := range []string{MediationRecordConfigMap, legacyMediationRecordConfigMap} {
		var cm corev1.ConfigMap
		err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: name}, &cm)
		if err == nil {
			return MediationRecord{Enabled: true}, nil
		}
		if !apierrors.IsNotFound(err) {
			return MediationRecord{}, err
		}
	}
	return MediationRecord{}, nil
}
