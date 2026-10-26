package controller

import (
	"context"
	"slices"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

func TestScheduleRouter_CreateUsesEventNamespace(t *testing.T) {
	agent := func(namespace string) *sympoziumv1alpha1.Agent {
		return &sympoziumv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "researcher", Namespace: namespace},
		}
	}

	tests := []struct {
		name          string
		agents        []client.Object
		metadata      map[string]string
		wantNamespace string
	}{
		{
			name:          "event namespace picks the same-named Agent",
			agents:        []client.Object{agent("team-a"), agent("team-b")},
			metadata:      map[string]string{"instanceName": "researcher", "namespace": "team-b"},
			wantNamespace: "team-b",
		},
		{
			name:          "no event namespace finds the Agent by name",
			agents:        []client.Object{agent("team-b")},
			metadata:      map[string]string{"instanceName": "researcher"},
			wantNamespace: "team-b",
		},
	}

	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.agents...).Build()
			sr := &ScheduleRouter{Client: cl, Log: logr.Discard()}

			event, err := eventbus.NewEvent(eventbus.TopicScheduleUpsert, tt.metadata, scheduleRequest{
				Name: "nightly", Action: "create", Schedule: "0 2 * * *", Task: "summarize the day",
			})
			if err != nil {
				t.Fatalf("build event: %v", err)
			}

			sr.handleScheduleEvent(context.Background(), event)

			var schedules sympoziumv1alpha1.SympoziumScheduleList
			if err := cl.List(context.Background(), &schedules); err != nil {
				t.Fatalf("list schedules: %v", err)
			}
			var got []string
			for _, s := range schedules.Items {
				got = append(got, s.Namespace+"/"+s.Name)
			}
			want := []string{tt.wantNamespace + "/researcher-nightly"}
			if !slices.Equal(got, want) {
				t.Errorf("schedules = %v, want %v", got, want)
			}
		})
	}
}
