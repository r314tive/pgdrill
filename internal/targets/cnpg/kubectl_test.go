package cnpg

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestKubectlClientCreateUsesManifestStdin(t *testing.T) {
	runner := &fakeCommandRunner{}
	client := NewKubectlClient(KubectlConfig{
		Binary:     "/usr/local/bin/kubectl",
		Namespace:  "override-ns",
		Kubeconfig: "/tmp/kubeconfig",
		Context:    "d003",
		Timeout:    2 * time.Minute,
	}, runner)
	spec := testVerifyClusterSpec(t)

	evidence, err := client.CreateCluster(context.Background(), spec, []byte("cluster-yaml"))
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}

	if len(runner.invocations) != 1 {
		t.Fatalf("expected one invocation, got %d", len(runner.invocations))
	}
	inv := runner.invocations[0]
	if inv.Path != "/usr/local/bin/kubectl" {
		t.Fatalf("unexpected kubectl path %q", inv.Path)
	}
	wantArgs := []string{"--kubeconfig", "/tmp/kubeconfig", "--context", "d003", "-n", "override-ns", "create", "-f", "-"}
	if !reflect.DeepEqual(inv.Args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", inv.Args, wantArgs)
	}
	if string(inv.Stdin) != "cluster-yaml" {
		t.Fatalf("unexpected stdin %q", string(inv.Stdin))
	}
	if inv.Timeout != 2*time.Minute {
		t.Fatalf("unexpected timeout %s", inv.Timeout)
	}
	if !hasOperation(evidence, "kubectl-create-cluster") {
		t.Fatalf("missing create evidence %#v", evidence)
	}
}

func TestKubectlClientFindOwnedClusterUsesAttemptOwnershipSelector(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	want := testOwnedCluster(t, spec)
	runner := &fakeCommandRunner{stdoutByArgContains: map[string]string{
		"get cluster.postgresql.cnpg.io": `{"items":[{"metadata":{"name":"` + spec.Name + `","uid":"cluster-uid","annotations":{"` + annotationRecoveryContract + `":"` + want.ContractDigest + `"}}}]}`,
	}}
	client := NewKubectlClient(KubectlConfig{}, runner)

	owned, evidence, err := client.FindOwnedCluster(context.Background(), spec)
	if err != nil {
		t.Fatalf("FindOwnedCluster() error = %v", err)
	}
	if !reflect.DeepEqual(owned, want) {
		t.Fatalf("FindOwnedCluster() = %#v, want %#v", owned, want)
	}
	wantArgs := []string{"-n", "d003-db", "get", "cluster.postgresql.cnpg.io", "-l", labelOwnershipID + "=" + spec.OwnershipID, "-o", "json"}
	if len(runner.invocations) != 1 || !reflect.DeepEqual(runner.invocations[0].Args, wantArgs) {
		t.Fatalf("FindOwnedCluster() args = %#v, want %#v", runner.invocations, wantArgs)
	}
	if !hasOperation(evidence, "kubectl-find-owned-cluster") {
		t.Fatalf("missing reconciliation evidence %#v", evidence)
	}
}

func TestKubectlClientFindOwnedClusterRejectsRecoveryContractMismatch(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	runner := &fakeCommandRunner{stdoutByArgContains: map[string]string{
		"get cluster.postgresql.cnpg.io": `{"items":[{"metadata":{"name":"` +
			spec.Name +
			`","uid":"cluster-uid","annotations":{"` +
			annotationRecoveryContract +
			`":"sha256:` +
			strings.Repeat("0", 64) +
			`"}}}]}`,
	}}
	client := NewKubectlClient(KubectlConfig{}, runner)

	if _, _, err := client.FindOwnedCluster(context.Background(), spec); err == nil ||
		!strings.Contains(err.Error(), "recovery contract digest does not match") {
		t.Fatalf("FindOwnedCluster() error = %v, want contract mismatch", err)
	}
}

func TestKubectlClientFindOwnedPVCsUsesAttemptOwnershipSelector(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	cluster := testOwnedCluster(t, spec)
	runner := &fakeCommandRunner{stdoutByArgContains: map[string]string{
		"get pvc": `{"items":[{"metadata":{
			"name":"` + spec.Name + `-1",
			"uid":"pvc-uid",
			"annotations":{"` + annotationRecoveryContract + `":"` + cluster.ContractDigest + `"},
			"ownerReferences":[{
				"apiVersion":"postgresql.cnpg.io/v1",
				"kind":"Cluster",
				"name":"` + spec.Name + `",
				"uid":"cluster-uid"
			}]
		}}]}`,
	}}
	client := NewKubectlClient(KubectlConfig{}, runner)

	owned, evidence, err := client.FindOwnedPVCs(context.Background(), spec)
	if err != nil {
		t.Fatalf("FindOwnedPVCs() error = %v", err)
	}
	want := OwnedPVCs{Items: []OwnedPVC{{
		Name:             spec.Name + "-1",
		UID:              "pvc-uid",
		OwnerClusterName: spec.Name,
		OwnerClusterUID:  "cluster-uid",
		ContractDigest:   cluster.ContractDigest,
	}}}
	if !reflect.DeepEqual(owned, want) {
		t.Fatalf("FindOwnedPVCs() = %#v, want %#v", owned, want)
	}
	wantArgs := []string{"-n", "d003-db", "get", "pvc", "-l", "cnpg.io/cluster=" + spec.Name + "," + labelOwnershipID + "=" + spec.OwnershipID, "-o", "json"}
	if len(runner.invocations) != 1 || !reflect.DeepEqual(runner.invocations[0].Args, wantArgs) {
		t.Fatalf("FindOwnedPVCs() args = %#v, want %#v", runner.invocations, wantArgs)
	}
	if !hasOperation(evidence, "kubectl-find-owned-pvcs") {
		t.Fatalf("missing PVC observation evidence %#v", evidence)
	}
}

func TestKubectlClientOwnedResourceObservationFailsClosedOnUnknownList(t *testing.T) {
	spec := testVerifyClusterSpec(t)

	clusterClient := NewKubectlClient(KubectlConfig{}, &fakeCommandRunner{stdoutByArgContains: map[string]string{
		"get cluster.postgresql.cnpg.io": `{}`,
	}})
	if _, _, err := clusterClient.FindOwnedCluster(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "items array is missing or null") {
		t.Fatalf("FindOwnedCluster() error = %v, want unknown list error", err)
	}

	pvcClient := NewKubectlClient(KubectlConfig{}, &fakeCommandRunner{stdoutByArgContains: map[string]string{
		"get pvc": `{"items":null}`,
	}})
	if _, _, err := pvcClient.FindOwnedPVCs(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "items array is missing or null") {
		t.Fatalf("FindOwnedPVCs() error = %v, want unknown list error", err)
	}
}

func TestKubectlClientOwnedResourceObservationRequiresUIDAndExactPVCOwner(t *testing.T) {
	spec := testVerifyClusterSpec(t)

	clusterClient := NewKubectlClient(KubectlConfig{}, &fakeCommandRunner{stdoutByArgContains: map[string]string{
		"get cluster.postgresql.cnpg.io": `{"items":[{"metadata":{"name":"` + spec.Name + `"}}]}`,
	}})
	if _, _, err := clusterClient.FindOwnedCluster(context.Background(), spec); err == nil ||
		!strings.Contains(err.Error(), "invalid metadata.uid") {
		t.Fatalf("FindOwnedCluster() error = %v, want missing uid refusal", err)
	}

	pvcClient := NewKubectlClient(KubectlConfig{}, &fakeCommandRunner{stdoutByArgContains: map[string]string{
		"get pvc": `{"items":[{"metadata":{"name":"` + spec.Name + `-1","uid":"pvc-uid"}}]}`,
	}})
	if _, _, err := pvcClient.FindOwnedPVCs(context.Background(), spec); err == nil ||
		!strings.Contains(err.Error(), "has no postgresql.cnpg.io/v1 Cluster ownerReference") {
		t.Fatalf("FindOwnedPVCs() error = %v, want missing owner refusal", err)
	}
}

func TestKubectlClientFindOwnedPVCsRejectsAmbiguousResourceSet(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	runner := &fakeCommandRunner{stdoutByArgContains: map[string]string{
		"get pvc": `{"items":[
			{"metadata":{"name":"` + spec.Name + `-1","uid":"pvc-uid-1"}},
			{"metadata":{"name":"` + spec.Name + `-2","uid":"pvc-uid-2"}}
		]}`,
	}}
	client := NewKubectlClient(KubectlConfig{}, runner)

	if _, _, err := client.FindOwnedPVCs(context.Background(), spec); err == nil ||
		!strings.Contains(err.Error(), "maximum is 1") {
		t.Fatalf("FindOwnedPVCs() error = %v, want bounded ambiguity refusal", err)
	}
}

func TestKubectlClientWaitReturnsRunningInstance(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	cluster := testOwnedCluster(t, spec)
	runner := &fakeCommandRunner{
		stdoutByArgContains: map[string]string{
			"cnpg.io/jobRole=full-recovery": `{"items":[]}`,
			"get pod":                       testReadyPodJSON(t, spec, cluster),
		},
	}
	client := NewKubectlClient(KubectlConfig{}, runner)

	instance, evidence, err := client.WaitForInstanceReady(
		context.Background(),
		spec,
		WaitOptions{Timeout: 90 * time.Second, Cluster: cluster},
	)
	if err != nil {
		t.Fatalf("wait for instance: %v", err)
	}

	if len(runner.invocations) != 2 {
		t.Fatalf("expected two invocations, got %d", len(runner.invocations))
	}
	if got, want := runner.invocations[0].Args, []string{"-n", "d003-db", "get", "pods", "-l", "cnpg.io/cluster=" + spec.Name + ",cnpg.io/jobRole=full-recovery," + labelOwnershipID + "=" + spec.OwnershipID, "-o", "json"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected full-recovery args: got %#v want %#v", got, want)
	}
	if got, want := runner.invocations[1].Args, []string{"-n", "d003-db", "get", "pod", spec.InstancePodName, "-o", "json"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected instance pod args: got %#v want %#v", got, want)
	}
	if instance.PodName != spec.InstancePodName {
		t.Fatalf("unexpected pod name %q", instance.PodName)
	}
	if instance.Host != spec.Name+"-rw.d003-db.svc" {
		t.Fatalf("unexpected host %q", instance.Host)
	}
	if instance.ConnString != DefaultPodConnString {
		t.Fatalf("unexpected conn string %q", instance.ConnString)
	}
	if instance.OperatorVersion != "1.26.3" {
		t.Fatalf("unexpected operator version %q", instance.OperatorVersion)
	}
	if instance.UID != "pod-uid" ||
		instance.ClusterUID != cluster.UID ||
		instance.ContractDigest != cluster.ContractDigest {
		t.Fatalf("unexpected instance identity %#v", instance)
	}
	if !hasOperation(evidence, "kubectl-check-full-recovery") || !hasOperation(evidence, "kubectl-check-instance-ready") {
		t.Fatalf("missing wait evidence %#v", evidence)
	}
}

func TestKubectlClientWaitRejectsPodOwnedByReplacedCluster(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	cluster := testOwnedCluster(t, spec)
	pod := strings.Replace(
		testReadyPodJSON(t, spec, cluster),
		`"uid":"`+cluster.UID+`"}]`,
		`"uid":"replaced-cluster-uid"}]`,
		1,
	)
	runner := &fakeCommandRunner{stdoutByArgContains: map[string]string{
		"cnpg.io/jobRole=full-recovery": `{"items":[]}`,
		"get pod":                       pod,
	}}
	client := NewKubectlClient(KubectlConfig{}, runner)

	if _, _, err := client.WaitForInstanceReady(
		context.Background(),
		spec,
		WaitOptions{Timeout: time.Second, Cluster: cluster},
	); err == nil || !strings.Contains(err.Error(), "exactly one ownerReference") {
		t.Fatalf("WaitForInstanceReady() error = %v, want stale owner refusal", err)
	}
}

func TestKubectlClientWaitFailsFastWhenFullRecoveryFailed(t *testing.T) {
	runner := &fakeCommandRunner{
		stdoutByArgContains: map[string]string{
			"cnpg.io/jobRole=full-recovery": `{"items":[{"metadata":{"name":"verify-altbox-abc12345-1-full-recovery"},"status":{"phase":"Failed"}}]}`,
			"get pod":                       `{"status":{"conditions":[{"type":"Ready","status":"True"}]}}`,
		},
	}
	client := NewKubectlClient(KubectlConfig{}, runner)
	spec := testVerifyClusterSpec(t)
	cluster := testOwnedCluster(t, spec)

	_, evidence, err := client.WaitForInstanceReady(
		context.Background(),
		spec,
		WaitOptions{Timeout: 90 * time.Second, Cluster: cluster},
	)
	if err == nil || !strings.Contains(err.Error(), "full-recovery failed") {
		t.Fatalf("expected full-recovery failure, got %v", err)
	}
	if len(runner.invocations) != 1 {
		t.Fatalf("expected fail-fast after one invocation, got %d", len(runner.invocations))
	}
	if !hasOperation(evidence, "kubectl-check-full-recovery") {
		t.Fatalf("missing full-recovery evidence %#v", evidence)
	}
}

func TestKubectlClientWaitTimeoutBoundsEveryPollAndSleep(t *testing.T) {
	t.Run("in-flight kubectl poll", func(t *testing.T) {
		runner := &blockingCommandRunner{}
		client := NewKubectlClient(KubectlConfig{Timeout: 500 * time.Millisecond}, runner)
		spec := testVerifyClusterSpec(t)
		cluster := testOwnedCluster(t, spec)

		started := time.Now()
		_, evidence, err := client.WaitForInstanceReady(
			context.Background(),
			spec,
			WaitOptions{Timeout: 20 * time.Millisecond, PollInterval: time.Second, Cluster: cluster},
		)
		elapsed := time.Since(started)

		if err == nil || !strings.Contains(err.Error(), "timeout waiting for CNPG instance") {
			t.Fatalf("WaitForInstanceReady() error = %v", err)
		}
		if elapsed >= 250*time.Millisecond {
			t.Fatalf("hard wait deadline took %s", elapsed)
		}
		if len(runner.invocations) != 1 {
			t.Fatalf("poll invocations = %d, want 1", len(runner.invocations))
		}
		if timeout := runner.invocations[0].Timeout; timeout <= 0 || timeout > 50*time.Millisecond {
			t.Fatalf("bounded command timeout = %s", timeout)
		}
		if !hasOperation(evidence, "kubectl-check-full-recovery") {
			t.Fatalf("missing timed-out poll evidence %#v", evidence)
		}
	})

	t.Run("poll interval sleep", func(t *testing.T) {
		spec := testVerifyClusterSpec(t)
		cluster := testOwnedCluster(t, spec)
		runner := &fakeCommandRunner{stdoutByArgContains: map[string]string{
			"cnpg.io/jobRole=full-recovery": `{"items":[]}`,
			"get pod":                       strings.Replace(testReadyPodJSON(t, spec, cluster), `"status":"True"`, `"status":"False"`, 1),
		}}
		client := NewKubectlClient(KubectlConfig{Timeout: 500 * time.Millisecond}, runner)

		started := time.Now()
		_, _, err := client.WaitForInstanceReady(
			context.Background(),
			spec,
			WaitOptions{Timeout: 20 * time.Millisecond, PollInterval: time.Second, Cluster: cluster},
		)
		elapsed := time.Since(started)

		if err == nil || !strings.Contains(err.Error(), "timeout waiting for CNPG instance") {
			t.Fatalf("WaitForInstanceReady() error = %v", err)
		}
		if elapsed >= 250*time.Millisecond {
			t.Fatalf("bounded poll sleep took %s", elapsed)
		}
		if len(runner.invocations) != 2 {
			t.Fatalf("poll invocations = %d, want 2", len(runner.invocations))
		}
	})
}

func TestKubectlClientFullRecoveryObservationFailsClosed(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	cluster := testOwnedCluster(t, spec)
	tests := []struct {
		name       string
		stdout     string
		nonzero    bool
		runnerErr  bool
		wantDetail string
	}{
		{
			name:       "runner error",
			runnerErr:  true,
			wantDetail: "simulated kubectl error",
		},
		{
			name:       "nonzero exit",
			stdout:     `{"items":[]}`,
			nonzero:    true,
			wantDetail: "kubectl-check-full-recovery failed",
		},
		{
			name:       "missing items",
			stdout:     `{}`,
			wantDetail: "items array is missing or null",
		},
		{
			name:       "null items",
			stdout:     `{"items":null}`,
			wantDetail: "items array is missing or null",
		},
		{
			name:       "malformed item",
			stdout:     `{"items":[{"metadata":{"name":"recovery"}}]}`,
			wantDetail: "has no status.phase",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeCommandRunner{
				stdoutByArgContains: map[string]string{
					"cnpg.io/jobRole=full-recovery": tt.stdout,
					"get pod":                       testReadyPodJSON(t, spec, cluster),
				},
			}
			if tt.nonzero {
				runner.failWhenArgsContain = "cnpg.io/jobRole=full-recovery"
			}
			if tt.runnerErr {
				runner.errorWhenArgsContain = "cnpg.io/jobRole=full-recovery"
			}
			client := NewKubectlClient(KubectlConfig{}, runner)

			_, _, err := client.WaitForInstanceReady(
				context.Background(),
				spec,
				WaitOptions{Timeout: time.Second, Cluster: cluster},
			)
			if err == nil || !strings.Contains(err.Error(), tt.wantDetail) {
				t.Fatalf("WaitForInstanceReady() error = %v, want %q", err, tt.wantDetail)
			}
			if len(runner.invocations) != 1 {
				t.Fatalf("full-recovery uncertainty must fail before Ready check: %#v", runner.invocations)
			}
		})
	}
}

func TestKubectlClientCaptureEvidenceIsBestEffort(t *testing.T) {
	runner := &fakeCommandRunner{
		failWhenArgsContain: "logs",
		stdoutByArgContains: map[string]string{
			"get events": "event one\nevent two\nevent three\n",
		},
	}
	client := NewKubectlClient(KubectlConfig{}, runner)
	spec := testVerifyClusterSpec(t)

	evidence, err := client.CaptureEvidence(context.Background(), spec, Instance{PodName: spec.InstancePodName}, CaptureOptions{
		Reason:          "start-failed",
		EventsTail:      2,
		PostgresLogTail: 5000,
	})
	if err != nil {
		t.Fatalf("capture evidence should be best effort, got %v", err)
	}

	if len(runner.invocations) != 11 {
		t.Fatalf("expected eleven capture invocations, got %d", len(runner.invocations))
	}
	if !hasOperation(evidence, "kubectl-capture-cluster-yaml") ||
		!hasOperation(evidence, "kubectl-capture-instance-describe") ||
		!hasOperation(evidence, "kubectl-capture-full-recovery-describe") ||
		!hasOperation(evidence, "kubectl-capture-full-recovery-log") ||
		!hasOperation(evidence, "kubectl-capture-full-recovery-bootstrap-log") ||
		!hasOperation(evidence, "kubectl-capture-postgres-describe") ||
		!hasOperation(evidence, "kubectl-capture-postgres-log") ||
		!hasOperation(evidence, "kubectl-capture-postgres-bootstrap-log") ||
		!hasOperation(evidence, "kubectl-capture-summary") {
		t.Fatalf("missing capture evidence %#v", evidence)
	}

	summary := evidence[len(evidence)-1]
	if summary.Attributes["capture_status"] != "warning" || summary.Attributes["best_effort"] != "true" {
		t.Fatalf("unexpected capture summary %#v", summary.Attributes)
	}
	if !strings.Contains(summary.Attributes["capture_error"], "kubectl-capture-full-recovery-log") {
		t.Fatalf("expected capture error in summary, got %#v", summary.Attributes)
	}
	if got := commandStdoutForOperation(evidence, "kubectl-capture-events"); got != "event two\nevent three\n" {
		t.Fatalf("expected tailed event evidence, got %q", got)
	}
	events := commandEvidenceForOperation(evidence, "kubectl-capture-events")
	if events == nil || !events.StdoutTruncated || events.StdoutBytes != int64(len("event one\nevent two\nevent three\n")) {
		t.Fatalf("expected explicit event truncation metadata, got %#v", events)
	}
}

func TestKubectlClientDeletePVCUsesObservedUIDPrecondition(t *testing.T) {
	runner := &fakeCommandRunner{}
	client := NewKubectlClient(KubectlConfig{Timeout: 10 * time.Minute}, runner)
	spec := testVerifyClusterSpec(t)
	cluster := testOwnedCluster(t, spec)
	owned := OwnedPVCs{Items: []OwnedPVC{{
		Name:             spec.Name + "-1",
		UID:              "pvc-uid",
		OwnerClusterName: spec.Name,
		OwnerClusterUID:  cluster.UID,
		ContractDigest:   cluster.ContractDigest,
	}}}

	evidence, err := client.DeletePVCs(context.Background(), spec, cluster, owned)
	if err != nil {
		t.Fatalf("delete pvcs: %v", err)
	}

	if len(runner.invocations) != 2 {
		t.Fatalf("expected delete and wait invocations, got %d", len(runner.invocations))
	}
	wantDeleteArgs := []string{
		"-n", "d003-db", "delete",
		"--raw=/api/v1/namespaces/d003-db/persistentvolumeclaims/" + spec.Name + "-1",
		"-f", "-",
	}
	wantWaitArgs := []string{
		"-n", "d003-db", "wait", "--for=delete",
		"persistentvolumeclaim/" + spec.Name + "-1",
		"--timeout=600s",
	}
	if !reflect.DeepEqual(runner.invocations[0].Args, wantDeleteArgs) ||
		!reflect.DeepEqual(runner.invocations[1].Args, wantWaitArgs) {
		t.Fatalf("unexpected delete invocations %#v", runner.invocations)
	}
	wantPayload := `{"apiVersion":"v1","kind":"DeleteOptions","propagationPolicy":"Foreground","preconditions":{"uid":"pvc-uid"}}`
	if string(runner.invocations[0].Stdin) != wantPayload {
		t.Fatalf("delete payload = %q, want %q", runner.invocations[0].Stdin, wantPayload)
	}
	if !hasOperation(evidence, "kubectl-delete-pvc") ||
		!hasOperation(evidence, "kubectl-wait-pvc-delete") {
		t.Fatalf("missing delete pvcs evidence %#v", evidence)
	}
}

func TestKubectlClientDeletePVCRejectsOwnerUIDMismatchBeforeMutation(t *testing.T) {
	runner := &fakeCommandRunner{}
	client := NewKubectlClient(KubectlConfig{}, runner)
	spec := testVerifyClusterSpec(t)
	cluster := ownedClusterForSpec(spec, "current-cluster-uid")
	owned := OwnedPVCs{Items: []OwnedPVC{{
		Name:             spec.Name + "-1",
		UID:              "pvc-uid",
		OwnerClusterName: spec.Name,
		OwnerClusterUID:  "replaced-cluster-uid",
		ContractDigest:   cluster.ContractDigest,
	}}}

	if _, err := client.DeletePVCs(context.Background(), spec, cluster, owned); err == nil ||
		!strings.Contains(err.Error(), "owner uid does not match") {
		t.Fatalf("DeletePVCs() error = %v, want owner uid mismatch", err)
	}
	if len(runner.invocations) != 0 {
		t.Fatalf("owner uid mismatch invoked kubectl: %#v", runner.invocations)
	}
}

func TestKubectlClientDeleteClusterUsesObservedUIDPrecondition(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	runner := &fakeCommandRunner{}
	client := NewKubectlClient(KubectlConfig{Timeout: 10 * time.Minute}, runner)
	owned := testOwnedCluster(t, spec)

	evidence, err := client.DeleteCluster(context.Background(), spec, owned)
	if err != nil {
		t.Fatalf("delete cluster: %v", err)
	}
	wantDeleteArgs := []string{
		"-n", "d003-db", "delete",
		"--raw=/apis/postgresql.cnpg.io/v1/namespaces/d003-db/clusters/" + spec.Name,
		"-f", "-",
	}
	wantWaitArgs := []string{
		"-n", "d003-db", "wait", "--for=delete",
		"cluster.postgresql.cnpg.io/" + spec.Name,
		"--timeout=600s",
	}
	if len(runner.invocations) != 2 ||
		!reflect.DeepEqual(runner.invocations[0].Args, wantDeleteArgs) ||
		!reflect.DeepEqual(runner.invocations[1].Args, wantWaitArgs) {
		t.Fatalf("unexpected delete invocation %#v", runner.invocations)
	}
	wantPayload := `{"apiVersion":"v1","kind":"DeleteOptions","propagationPolicy":"Foreground","preconditions":{"uid":"cluster-uid"}}`
	if string(runner.invocations[0].Stdin) != wantPayload {
		t.Fatalf("delete payload = %q, want %q", runner.invocations[0].Stdin, wantPayload)
	}
	if !hasOperation(evidence, "kubectl-delete-cluster") ||
		!hasOperation(evidence, "kubectl-wait-cluster-delete") {
		t.Fatalf("missing delete cluster evidence %#v", evidence)
	}
}

func TestKubectlClientDeleteClusterIsIdempotentWhenOwnershipIsAbsent(t *testing.T) {
	runner := &fakeCommandRunner{}
	client := NewKubectlClient(KubectlConfig{}, runner)

	evidence, err := client.DeleteCluster(
		context.Background(),
		testVerifyClusterSpec(t),
		OwnedCluster{},
	)

	if err != nil {
		t.Fatalf("delete absent cluster: %v", err)
	}
	if len(runner.invocations) != 0 || len(evidence) != 0 {
		t.Fatalf("absent cluster must not invoke kubectl, got %#v", runner.invocations)
	}
}

func TestKubectlClientFindOwnedClusterFailsClosedOnAmbiguousOwnership(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	runner := &fakeCommandRunner{stdoutByArgContains: map[string]string{
		"get cluster.postgresql.cnpg.io": `{"items":[
			{"metadata":{"name":"` + spec.Name + `","uid":"cluster-uid"}},
			{"metadata":{"name":"unexpected-cluster","uid":"other-uid"}}
		]}`,
	}}
	client := NewKubectlClient(KubectlConfig{}, runner)

	_, evidence, err := client.FindOwnedCluster(context.Background(), spec)

	if err == nil || !strings.Contains(err.Error(), "maximum is 1") {
		t.Fatalf("FindOwnedCluster() error = %v", err)
	}
	if len(runner.invocations) != 1 {
		t.Fatalf("ambiguous ownership observation invocations = %#v", runner.invocations)
	}
	if !hasOperation(evidence, "kubectl-find-owned-cluster") {
		t.Fatalf("missing ownership observation evidence %#v", evidence)
	}
}

func TestKubectlClientCleanupRequiresOwnershipID(t *testing.T) {
	runner := &fakeCommandRunner{}
	client := NewKubectlClient(KubectlConfig{}, runner)
	spec := testVerifyClusterSpec(t)
	cluster := testOwnedCluster(t, spec)
	spec.OwnershipID = ""

	if _, err := client.DeleteCluster(context.Background(), spec, cluster); err == nil || !strings.Contains(err.Error(), "ownership id is required") {
		t.Fatalf("expected cluster ownership guard, got %v", err)
	}
	if _, err := client.DeletePVCs(context.Background(), spec, cluster, OwnedPVCs{}); err == nil || !strings.Contains(err.Error(), "ownership id is required") {
		t.Fatalf("expected PVC ownership guard, got %v", err)
	}
	if _, _, err := client.FindOwnedPVCs(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "ownership id is required") {
		t.Fatalf("expected PVC observation ownership guard, got %v", err)
	}
	if len(runner.invocations) != 0 {
		t.Fatalf("ownership guard must fail before kubectl, got %#v", runner.invocations)
	}

	spec.OwnershipID = "owner,team=other"
	if _, err := client.DeleteCluster(context.Background(), spec, cluster); err == nil || !strings.Contains(err.Error(), "safe label value") {
		t.Fatalf("expected selector injection guard, got %v", err)
	}
	if len(runner.invocations) != 0 {
		t.Fatalf("selector injection guard must fail before kubectl, got %#v", runner.invocations)
	}
}

func TestKubectlClientLatestCompletedBackupSelectsNewest(t *testing.T) {
	runner := &fakeCommandRunner{
		stdoutByArgContains: map[string]string{
			"backups.postgresql.cnpg.io": `{
  "items": [
    {
      "metadata": {"name": "altbox-old", "creationTimestamp": "2026-07-06T01:00:00Z"},
      "spec": {"cluster": {"name": "altbox"}},
      "status": {"phase": "completed"}
    },
    {
      "metadata": {"name": "altbox-running", "creationTimestamp": "2026-07-07T01:00:00Z"},
      "spec": {"cluster": {"name": "altbox"}},
      "status": {"phase": "running"}
    },
    {
      "metadata": {"name": "other-new", "creationTimestamp": "2026-07-08T01:00:00Z"},
      "spec": {"cluster": {"name": "other"}},
      "status": {"phase": "completed"}
    },
    {
      "metadata": {"name": "altbox-new", "creationTimestamp": "2026-07-07T02:00:00Z"},
      "spec": {"cluster": {"name": "altbox"}},
      "status": {"phase": "completed"}
    }
  ]
}`,
		},
	}
	client := NewKubectlClient(KubectlConfig{}, runner)
	spec := testVerifyClusterSpec(t)

	backupName, evidence, err := client.LatestCompletedBackup(context.Background(), spec)
	if err != nil {
		t.Fatalf("latest completed backup: %v", err)
	}

	if backupName != "altbox-new" {
		t.Fatalf("unexpected backup name %q", backupName)
	}
	if !hasOperation(evidence, "kubectl-discover-cnpg-backups") {
		t.Fatalf("missing discovery evidence %#v", evidence)
	}
	if got := runner.invocations[0].Args; !reflect.DeepEqual(got, []string{"-n", "d003-db", "get", "backups.postgresql.cnpg.io", "-o", "json"}) {
		t.Fatalf("unexpected args %#v", got)
	}
}

func TestKubectlClientCompletedBackupRetainsPluginIdentity(t *testing.T) {
	fixture := string(mustReadCNPGFixture(t, "backups-plugin.json"))
	runner := &fakeCommandRunner{
		stdoutByArgContains: map[string]string{
			"backups.postgresql.cnpg.io": fixture,
		},
	}
	client := NewKubectlClient(KubectlConfig{}, runner)
	spec := testVerifyClusterSpec(t)

	latest, evidence, err := client.CompletedBackup(context.Background(), spec, "")
	if err != nil {
		t.Fatalf("discover latest plugin backup: %v", err)
	}
	if latest.Name != "altbox-plugin-new" ||
		latest.BackupID != "20260707T020304" ||
		latest.Method != "plugin" ||
		latest.PluginName != DefaultPluginName ||
		latest.PluginVersion != "0.13.0" {
		t.Fatalf("unexpected latest plugin backup %#v", latest)
	}
	if !hasOperation(evidence, "kubectl-discover-cnpg-backups") {
		t.Fatalf("missing backup discovery evidence %#v", evidence)
	}

	exact, _, err := client.CompletedBackup(context.Background(), spec, "altbox-plugin-old")
	if err != nil {
		t.Fatalf("discover exact plugin backup: %v", err)
	}
	if exact.Name != "altbox-plugin-old" || exact.BackupID != "20260706T010002" {
		t.Fatalf("unexpected exact plugin backup %#v", exact)
	}
}

func TestKubectlClientCompletedBackupRejectsInvalidExactSelection(t *testing.T) {
	fixture := string(mustReadCNPGFixture(t, "backups-plugin.json"))
	tests := []struct {
		name   string
		backup string
		want   string
	}{
		{name: "not completed", backup: "altbox-plugin-running", want: "not completed"},
		{name: "different source cluster", backup: "other-plugin-new", want: `not "altbox"`},
		{name: "missing", backup: "missing", want: "not found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeCommandRunner{
				stdoutByArgContains: map[string]string{
					"backups.postgresql.cnpg.io": fixture,
				},
			}
			client := NewKubectlClient(KubectlConfig{}, runner)
			_, _, err := client.CompletedBackup(context.Background(), testVerifyClusterSpec(t), test.backup)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CompletedBackup() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestKubectlClientRedactsRawDiscoveryParseError(t *testing.T) {
	const secret = "cnpg-discovery-secret"
	runner := &fakeCommandRunner{
		stdoutByArgContains: map[string]string{
			"backups.postgresql.cnpg.io": `{
  "items": [{
    "metadata": {
      "name": "backup",
      "creationTimestamp": "` + secret + `"
    },
    "spec": {"cluster": {"name": "altbox"}},
    "status": {"phase": "completed"}
  }]
}`,
		},
	}
	client := NewKubectlClient(KubectlConfig{RedactValues: []string{secret}}, runner)

	_, evidence, err := client.CompletedBackup(
		context.Background(),
		testVerifyClusterSpec(t),
		"",
	)
	if err == nil || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("CompletedBackup() error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("CompletedBackup() error leaked configured value: %v", err)
	}
	if len(evidence) != 1 ||
		evidence[0].Command == nil ||
		strings.Contains(evidence[0].Command.Stdout, secret) {
		t.Fatalf("CompletedBackup() evidence leaked configured value: %#v", evidence)
	}
}

func TestKubectlClientRejectsRedactedCanonicalDiscoveryFields(t *testing.T) {
	const secret = "canonical-secret"
	spec := testVerifyClusterSpec(t)

	t.Run("backup name", func(t *testing.T) {
		runner := &fakeCommandRunner{
			stdoutByArgContains: map[string]string{
				"backups.postgresql.cnpg.io": `{
  "items": [{
    "metadata": {
      "name": "backup-` + secret + `",
      "creationTimestamp": "2026-07-07T02:00:00Z"
    },
    "spec": {"cluster": {"name": "altbox"}},
    "status": {"phase": "completed"}
  }]
}`,
			},
		}
		client := NewKubectlClient(
			KubectlConfig{RedactValues: []string{secret}},
			runner,
		)

		_, _, err := client.CompletedBackup(context.Background(), spec, "")
		if err == nil || !strings.Contains(err.Error(), `canonical field "backup_name"`) {
			t.Fatalf("CompletedBackup() error = %v", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("CompletedBackup() error leaked configured value: %v", err)
		}
	})

	t.Run("postgres image", func(t *testing.T) {
		runner := &fakeCommandRunner{
			stdoutByArgContains: map[string]string{
				"cluster.postgresql.cnpg.io": `{
  "spec": {"imageName": "registry.example/postgres:` + secret + `"}
}`,
			},
		}
		client := NewKubectlClient(
			KubectlConfig{RedactValues: []string{secret}},
			runner,
		)

		_, _, err := client.SourceClusterImage(context.Background(), spec)
		if err == nil || !strings.Contains(err.Error(), `canonical field "postgres_image"`) {
			t.Fatalf("SourceClusterImage() error = %v", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("SourceClusterImage() error leaked configured value: %v", err)
		}
	})

	t.Run("plugin object store", func(t *testing.T) {
		runner := &fakeCommandRunner{
			stdoutByArgContains: map[string]string{
				"cluster.postgresql.cnpg.io": `{
  "spec": {
    "plugins": [{
      "name": "` + DefaultPluginName + `",
      "isWALArchiver": true,
      "parameters": {
        "barmanObjectName": "store-` + secret + `",
        "serverName": "altbox"
      }
    }]
  }
}`,
			},
		}
		client := NewKubectlClient(
			KubectlConfig{RedactValues: []string{secret}},
			runner,
		)

		_, _, err := client.SourceClusterPlugin(
			context.Background(),
			spec,
			DefaultPluginName,
		)
		if err == nil || !strings.Contains(err.Error(), `canonical field "object_store"`) {
			t.Fatalf("SourceClusterPlugin() error = %v", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("SourceClusterPlugin() error leaked configured value: %v", err)
		}
	})
}

func TestKubectlClientSourceClusterImage(t *testing.T) {
	runner := &fakeCommandRunner{
		stdoutByArgContains: map[string]string{
			"cluster.postgresql.cnpg.io": `{"spec":{"imageName":"ghcr.io/cloudnative-pg/postgresql:16.4"}}`,
		},
	}
	client := NewKubectlClient(KubectlConfig{}, runner)
	spec := testVerifyClusterSpec(t)

	image, evidence, err := client.SourceClusterImage(context.Background(), spec)
	if err != nil {
		t.Fatalf("source cluster image: %v", err)
	}

	if image != "ghcr.io/cloudnative-pg/postgresql:16.4" {
		t.Fatalf("unexpected image %q", image)
	}
	if !hasOperation(evidence, "kubectl-discover-cnpg-source-image") {
		t.Fatalf("missing image discovery evidence %#v", evidence)
	}
}

func TestKubectlClientSourceClusterPlugin(t *testing.T) {
	runner := &fakeCommandRunner{
		stdoutByArgContains: map[string]string{
			"cluster.postgresql.cnpg.io": string(mustReadCNPGFixture(t, "cluster-plugin.json")),
		},
	}
	client := NewKubectlClient(KubectlConfig{}, runner)
	spec := testVerifyClusterSpec(t)

	source, evidence, err := client.SourceClusterPlugin(context.Background(), spec, DefaultPluginName)
	if err != nil {
		t.Fatalf("source cluster plugin: %v", err)
	}
	if source.PluginName != DefaultPluginName ||
		source.ObjectStore != "altbox-backups" ||
		source.ServerName != "altbox-archive" ||
		!source.WALArchiver {
		t.Fatalf("unexpected plugin source %#v", source)
	}
	if !hasOperation(evidence, "kubectl-discover-cnpg-source-plugin") {
		t.Fatalf("missing source plugin evidence %#v", evidence)
	}
}

func TestParseClusterPluginDefaultsServerName(t *testing.T) {
	source, err := parseClusterPlugin([]byte(`{
  "spec": {
    "plugins": [{
      "name": "barman-cloud.cloudnative-pg.io",
      "isWALArchiver": true,
      "parameters": {"barmanObjectName": "backups"}
    }]
  }
}`), DefaultPluginName, "altbox")
	if err != nil {
		t.Fatalf("parse source plugin: %v", err)
	}
	if source.ServerName != "altbox" {
		t.Fatalf("server name = %q, want source cluster fallback", source.ServerName)
	}
}

func TestParseClusterPluginRejectsUnusableConfiguration(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "disabled",
			data: `{"spec":{"plugins":[{
			  "name":"barman-cloud.cloudnative-pg.io",
			  "enabled":false,
			  "parameters":{"barmanObjectName":"backups"}
			}]}}`,
			want: "disabled",
		},
		{
			name: "missing object store",
			data: `{"spec":{"plugins":[{
			  "name":"barman-cloud.cloudnative-pg.io",
			  "isWALArchiver":true,
			  "parameters":{}
			}]}}`,
			want: "no barmanObjectName",
		},
		{
			name: "missing plugin",
			data: `{"spec":{"plugins":[]}}`,
			want: "has no plugin",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseClusterPlugin([]byte(test.data), DefaultPluginName, "altbox")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseClusterPlugin() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestKubectlClientSourceClusterImageFallsBackToPostgresPod(t *testing.T) {
	runner := &fakeCommandRunner{
		stdoutByArgContains: map[string]string{
			"cluster.postgresql.cnpg.io": `{"spec":{}}`,
			"get pods":                   `{"items":[{"spec":{"containers":[{"name":"manager","image":"manager:v1"},{"name":"postgres","image":"ghcr.io/cloudnative-pg/postgresql:16.5"}]}}]}`,
		},
	}
	client := NewKubectlClient(KubectlConfig{}, runner)
	spec := testVerifyClusterSpec(t)

	image, evidence, err := client.SourceClusterImage(context.Background(), spec)
	if err != nil {
		t.Fatalf("source cluster image fallback: %v", err)
	}
	if image != "ghcr.io/cloudnative-pg/postgresql:16.5" {
		t.Fatalf("unexpected fallback image %q", image)
	}
	if len(runner.invocations) != 2 {
		t.Fatalf("expected cluster and pod discovery, got %d invocations", len(runner.invocations))
	}
	if !hasOperation(evidence, "kubectl-discover-cnpg-source-image") || !hasOperation(evidence, "kubectl-discover-cnpg-source-pod-image") {
		t.Fatalf("missing fallback discovery evidence %#v", evidence)
	}
}

type fakeCommandRunner struct {
	invocations          []command.Invocation
	failWhenArgsContain  string
	errorWhenArgsContain string
	stdoutByArgContains  map[string]string
}

func (r *fakeCommandRunner) Run(_ context.Context, inv command.Invocation) (command.Result, error) {
	r.invocations = append(r.invocations, inv)
	if r.errorWhenArgsContain != "" &&
		strings.Contains(strings.Join(inv.Args, " "), r.errorWhenArgsContain) {
		return command.Result{}, errors.New("simulated kubectl error")
	}

	success := true
	exitCode := 0
	if r.failWhenArgsContain != "" && strings.Contains(strings.Join(inv.Args, " "), r.failWhenArgsContain) {
		success = false
		exitCode = 1
	}

	stdout := "ok\n"
	for _, marker := range sortedKeys(r.stdoutByArgContains) {
		value := r.stdoutByArgContains[marker]
		if strings.Contains(strings.Join(inv.Args, " "), marker) {
			stdout = value
			break
		}
	}

	now := time.Date(2026, 7, 7, 8, 40, 0, 0, time.UTC).Add(time.Duration(len(r.invocations)) * time.Second)
	redactor := command.NewRedactor(inv.RedactValues...)
	evidenceArgs := make([]string, len(inv.Args))
	for index, argument := range inv.Args {
		evidenceArgs[index] = redactor.RedactString(argument)
	}
	return command.Result{
		Raw: command.RawEvidence{
			Path:   inv.Path,
			Args:   append([]string{}, inv.Args...),
			Stdout: []byte(stdout),
		},
		Evidence: model.CommandEvidence{
			Path:       redactor.RedactString(inv.Path),
			Args:       evidenceArgs,
			StartedAt:  now.Add(-1 * time.Second),
			FinishedAt: now,
			ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				Success:  success,
				ExitCode: exitCode,
			},
			Stdout: redactor.RedactString(stdout),
		},
	}, nil
}

type blockingCommandRunner struct {
	invocations []command.Invocation
}

func (r *blockingCommandRunner) Run(ctx context.Context, inv command.Invocation) (command.Result, error) {
	r.invocations = append(r.invocations, inv)
	<-ctx.Done()
	now := time.Now().UTC()
	return command.Result{
		Evidence: model.CommandEvidence{
			Path:       inv.Path,
			Args:       append([]string{}, inv.Args...),
			StartedAt:  now,
			FinishedAt: now,
			ExitStatus: model.ExitStatus{
				ExitCode: -1,
				TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded),
				Canceled: errors.Is(ctx.Err(), context.Canceled),
				Error:    ctx.Err().Error(),
			},
		},
	}, ctx.Err()
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) == len(keys[j]) {
			return keys[i] < keys[j]
		}
		return len(keys[i]) > len(keys[j])
	})
	return keys
}

func mustReadCNPGFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func commandStdoutForOperation(records []model.EvidenceRecord, operation string) string {
	commandEvidence := commandEvidenceForOperation(records, operation)
	if commandEvidence != nil {
		return commandEvidence.Stdout
	}
	return ""
}

func commandEvidenceForOperation(records []model.EvidenceRecord, operation string) *model.CommandEvidence {
	for _, record := range records {
		if record.Attributes["operation"] == operation && record.Command != nil {
			return record.Command
		}
	}
	return nil
}
