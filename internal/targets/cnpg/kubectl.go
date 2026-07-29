package cnpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/jsonutil"
	"github.com/r314tive/pgdrill/internal/model"
)

const defaultKubectlBinary = "kubectl"

const (
	maxOwnedPVCResources  = 1
	maxKubernetesUIDBytes = 128
)

type KubectlConfig struct {
	Binary       string
	Namespace    string
	Kubeconfig   string
	Context      string
	Timeout      time.Duration
	RedactValues []string
}

type KubectlClient struct {
	cfg    KubectlConfig
	runner command.Runner
}

type canonicalField struct {
	name  string
	value string
}

func rejectRedactedCanonicalFields(
	result command.Result,
	scope string,
	fields ...canonicalField,
) error {
	for _, field := range fields {
		if field.value != result.RedactString(field.value) {
			return fmt.Errorf(
				"%s canonical field %q contains a configured redaction value",
				scope,
				field.name,
			)
		}
	}
	return nil
}

func NewKubectlClient(cfg KubectlConfig, runner command.Runner) *KubectlClient {
	if runner == nil {
		runner = command.NewRunner(command.Options{DefaultTimeout: cfg.Timeout})
	}
	return &KubectlClient{
		cfg:    cfg,
		runner: runner,
	}
}

func (c *KubectlClient) CreateCluster(ctx context.Context, spec VerifyClusterSpec, manifest []byte) ([]model.EvidenceRecord, error) {
	return c.strictRun(ctx, "kubectl-create-cluster", c.args(spec, "create", "-f", "-"), manifest, c.cfg.Timeout)
}

func (c *KubectlClient) FindOwnedCluster(ctx context.Context, spec VerifyClusterSpec) (OwnedCluster, []model.EvidenceRecord, error) {
	selector, err := ownershipSelector(spec, false)
	if err != nil {
		return OwnedCluster{}, nil, fmt.Errorf("find owned cluster: %w", err)
	}
	evidence, result, err := c.run(ctx, "kubectl-find-owned-cluster", c.args(spec, "get", "cluster.postgresql.cnpg.io", "-l", selector, "-o", "json"), nil, c.cfg.Timeout)
	if err != nil {
		return OwnedCluster{}, evidence, err
	}
	if !result.Evidence.ExitStatus.Success {
		return OwnedCluster{}, evidence, fmt.Errorf("kubectl-find-owned-cluster failed: %s", result.Evidence.ExitStatus.Summary())
	}
	resources, err := parseOwnedResources(result.Raw.Stdout, "CNPG cluster", 1)
	if err != nil {
		return OwnedCluster{}, evidence, result.RedactError(err)
	}
	if len(resources) == 0 {
		return OwnedCluster{}, evidence, nil
	}
	resource := resources[0]
	owned := OwnedCluster{
		Found:          true,
		Name:           resource.Name,
		UID:            resource.UID,
		ContractDigest: strings.TrimSpace(resource.Annotations[annotationRecoveryContract]),
	}
	if err := validateOwnedCluster(spec, owned); err != nil {
		return OwnedCluster{}, evidence, result.RedactError(
			fmt.Errorf("validate owned CNPG cluster: %w", err),
		)
	}
	if err := rejectRedactedCanonicalFields(
		result,
		"owned CNPG cluster",
		canonicalField{name: "name", value: owned.Name},
		canonicalField{name: "uid", value: owned.UID},
		canonicalField{name: "recovery_contract", value: owned.ContractDigest},
	); err != nil {
		return OwnedCluster{}, evidence, err
	}
	return owned, evidence, nil
}

func (c *KubectlClient) FindOwnedPVCs(ctx context.Context, spec VerifyClusterSpec) (OwnedPVCs, []model.EvidenceRecord, error) {
	selector, err := ownershipSelector(spec, true)
	if err != nil {
		return OwnedPVCs{}, nil, fmt.Errorf("find owned PVCs: %w", err)
	}
	evidence, result, err := c.run(ctx, "kubectl-find-owned-pvcs", c.args(spec, "get", "pvc", "-l", selector, "-o", "json"), nil, c.cfg.Timeout)
	if err != nil {
		return OwnedPVCs{}, evidence, err
	}
	if !result.Evidence.ExitStatus.Success {
		return OwnedPVCs{}, evidence, fmt.Errorf("kubectl-find-owned-pvcs failed: %s", result.Evidence.ExitStatus.Summary())
	}
	resources, err := parseOwnedResources(result.Raw.Stdout, "PVC", maxOwnedPVCResources)
	if err != nil {
		return OwnedPVCs{}, evidence, result.RedactError(err)
	}
	owned := make([]OwnedPVC, 0, len(resources))
	contractDigest, err := spec.ContractDigest()
	if err != nil {
		return OwnedPVCs{}, evidence, err
	}
	for _, resource := range resources {
		owner, err := clusterOwner(resource, spec.Name)
		if err != nil {
			return OwnedPVCs{}, evidence, result.RedactError(
				fmt.Errorf("validate owned PVC %q: %w", resource.Name, err),
			)
		}
		pvc := OwnedPVC{
			Name:             resource.Name,
			UID:              resource.UID,
			OwnerClusterName: owner.Name,
			OwnerClusterUID:  owner.UID,
			ContractDigest:   strings.TrimSpace(resource.Annotations[annotationRecoveryContract]),
		}
		if pvc.ContractDigest != contractDigest {
			return OwnedPVCs{}, evidence, result.RedactError(
				fmt.Errorf(
					"validate owned PVC %q: recovery contract digest does not match",
					resource.Name,
				),
			)
		}
		if err := rejectRedactedCanonicalFields(
			result,
			"owned CNPG PVC",
			canonicalField{name: "name", value: pvc.Name},
			canonicalField{name: "uid", value: pvc.UID},
			canonicalField{name: "owner_cluster_name", value: pvc.OwnerClusterName},
			canonicalField{name: "owner_cluster_uid", value: pvc.OwnerClusterUID},
			canonicalField{name: "recovery_contract", value: pvc.ContractDigest},
		); err != nil {
			return OwnedPVCs{}, evidence, err
		}
		owned = append(owned, pvc)
	}
	return OwnedPVCs{Items: owned}, evidence, nil
}

type ownedResource struct {
	Name            string
	UID             string
	Annotations     map[string]string
	OwnerReferences []ownedResourceReference
}

type ownedResourceReference struct {
	APIVersion string
	Kind       string
	Name       string
	UID        string
}

func parseOwnedResources(data []byte, resource string, limit int) ([]ownedResource, error) {
	var list struct {
		Items *[]struct {
			Metadata struct {
				Name            string            `json:"name"`
				UID             string            `json:"uid"`
				Annotations     map[string]string `json:"annotations"`
				OwnerReferences []struct {
					APIVersion string `json:"apiVersion"`
					Kind       string `json:"kind"`
					Name       string `json:"name"`
					UID        string `json:"uid"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := jsonutil.DecodeOne(data, &list); err != nil {
		return nil, fmt.Errorf("parse owned %s list: %w", resource, err)
	}
	if list.Items == nil {
		return nil, fmt.Errorf("parse owned %s list: items array is missing or null", resource)
	}
	if len(*list.Items) > limit {
		return nil, fmt.Errorf("ownership selector matches %d %s resources; maximum is %d", len(*list.Items), resource, limit)
	}
	resources := make([]ownedResource, 0, len(*list.Items))
	seenNames := make(map[string]struct{}, len(*list.Items))
	seenUIDs := make(map[string]struct{}, len(*list.Items))
	for _, item := range *list.Items {
		name := strings.TrimSpace(item.Metadata.Name)
		if err := validateKubernetesResourceName(name); err != nil {
			return nil, fmt.Errorf("owned %s has invalid metadata.name: %w", resource, err)
		}
		uid := strings.TrimSpace(item.Metadata.UID)
		if err := validateKubernetesUID(uid); err != nil {
			return nil, fmt.Errorf("owned %s %q has invalid metadata.uid: %w", resource, name, err)
		}
		if _, exists := seenNames[name]; exists {
			return nil, fmt.Errorf("owned %s name %q is duplicated", resource, name)
		}
		if _, exists := seenUIDs[uid]; exists {
			return nil, fmt.Errorf("owned %s uid %q is duplicated", resource, uid)
		}
		seenNames[name] = struct{}{}
		seenUIDs[uid] = struct{}{}
		parsed := ownedResource{
			Name:        name,
			UID:         uid,
			Annotations: item.Metadata.Annotations,
		}
		for _, owner := range item.Metadata.OwnerReferences {
			parsed.OwnerReferences = append(parsed.OwnerReferences, ownedResourceReference{
				APIVersion: strings.TrimSpace(owner.APIVersion),
				Kind:       strings.TrimSpace(owner.Kind),
				Name:       strings.TrimSpace(owner.Name),
				UID:        strings.TrimSpace(owner.UID),
			})
		}
		resources = append(resources, parsed)
	}
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].Name < resources[j].Name
	})
	return resources, nil
}

func clusterOwner(resource ownedResource, clusterName string) (ownedResourceReference, error) {
	var matched *ownedResourceReference
	for index := range resource.OwnerReferences {
		owner := &resource.OwnerReferences[index]
		if owner.APIVersion != "postgresql.cnpg.io/v1" ||
			owner.Kind != "Cluster" ||
			owner.Name != clusterName {
			continue
		}
		if matched != nil {
			return ownedResourceReference{}, fmt.Errorf("has multiple matching CNPG Cluster ownerReferences")
		}
		matched = owner
	}
	if matched == nil {
		return ownedResourceReference{}, fmt.Errorf(
			"has no postgresql.cnpg.io/v1 Cluster ownerReference for %q",
			clusterName,
		)
	}
	if err := validateKubernetesUID(matched.UID); err != nil {
		return ownedResourceReference{}, fmt.Errorf("ownerReference uid is invalid: %w", err)
	}
	return *matched, nil
}

func (c *KubectlClient) WaitForInstanceReady(ctx context.Context, spec VerifyClusterSpec, opts WaitOptions) (Instance, []model.EvidenceRecord, error) {
	if err := validateOwnedCluster(spec, opts.Cluster); err != nil {
		return Instance{}, nil, fmt.Errorf("wait for CNPG instance: %w", err)
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultWaitTimeout
	}
	if timeout < 0 {
		return Instance{}, nil, fmt.Errorf("CNPG wait timeout must not be negative")
	}
	pollInterval := opts.PollInterval
	if pollInterval == 0 {
		pollInterval = DefaultPollInterval
	}
	if pollInterval < 0 {
		return Instance{}, nil, fmt.Errorf("CNPG poll interval must not be negative")
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pollEvidence := newPollEvidence()
	for {
		if err := waitCtx.Err(); err != nil {
			return Instance{}, pollEvidence.Records(), waitForInstanceError(ctx, waitCtx, spec)
		}
		commandTimeout := boundedPollTimeout(waitCtx, c.cfg.Timeout)
		failed, reason, recoveryEvidence, err := c.fullRecoveryFailed(
			waitCtx,
			spec,
			commandTimeout,
		)
		pollEvidence.Add(recoveryEvidence...)
		if err != nil {
			if waitCtx.Err() != nil {
				return Instance{}, pollEvidence.Records(), waitForInstanceError(ctx, waitCtx, spec)
			}
			return Instance{}, pollEvidence.Records(), err
		}
		if failed {
			return Instance{}, pollEvidence.Records(), fmt.Errorf("CNPG full-recovery failed before instance pod became Ready: %s", reason)
		}

		commandTimeout = boundedPollTimeout(waitCtx, c.cfg.Timeout)
		ready, operatorVersion, podEvidence, err := c.instancePodReady(
			waitCtx,
			spec,
			opts.Cluster,
			commandTimeout,
		)
		pollEvidence.Add(podEvidence...)
		if err != nil {
			if waitCtx.Err() != nil {
				return Instance{}, pollEvidence.Records(), waitForInstanceError(ctx, waitCtx, spec)
			}
			return Instance{}, pollEvidence.Records(), err
		}
		if ready {
			host := serviceHost(spec)
			return Instance{
				PodName:         spec.InstancePodName,
				UID:             operatorVersion.PodUID,
				ClusterUID:      opts.Cluster.UID,
				ContractDigest:  opts.Cluster.ContractDigest,
				Host:            host,
				Port:            DefaultPostgresPort,
				Database:        "postgres",
				ConnString:      DefaultPodConnString,
				OperatorVersion: operatorVersion.OperatorVersion,
			}, pollEvidence.Records(), nil
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return Instance{}, pollEvidence.Records(), waitForInstanceError(ctx, waitCtx, spec)
		case <-timer.C:
		}
	}
}

func boundedPollTimeout(ctx context.Context, configured time.Duration) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return configured
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return time.Nanosecond
	}
	if configured <= 0 || remaining < configured {
		return remaining
	}
	return configured
}

func waitForInstanceError(parent, waitCtx context.Context, spec VerifyClusterSpec) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf(
			"timeout waiting for CNPG instance pod %q to become Ready",
			spec.InstancePodName,
		)
	}
	return waitCtx.Err()
}

func (c *KubectlClient) fullRecoveryFailed(ctx context.Context, spec VerifyClusterSpec, timeout time.Duration) (bool, string, []model.EvidenceRecord, error) {
	args := c.args(
		spec,
		"get",
		"pods",
		"-l",
		"cnpg.io/cluster="+spec.Name+
			",cnpg.io/jobRole=full-recovery,"+
			labelOwnershipID+"="+spec.OwnershipID,
		"-o",
		"json",
	)
	evidence, result, err := c.run(ctx, "kubectl-check-full-recovery", args, nil, timeout)
	if err != nil {
		return false, "", evidence, err
	}
	if !result.Evidence.ExitStatus.Success {
		return false, "", evidence, fmt.Errorf(
			"kubectl-check-full-recovery failed: %s",
			result.Evidence.ExitStatus.Summary(),
		)
	}
	failed, reason, err := fullRecoveryFailed(result.Raw.Stdout)
	if err != nil {
		return false, "", evidence, result.RedactError(err)
	}
	if err := rejectRedactedCanonicalFields(
		result,
		"CNPG full-recovery observation",
		canonicalField{name: "pod_name", value: reason},
	); err != nil {
		return false, "", evidence, err
	}
	return failed, reason, evidence, nil
}

func (c *KubectlClient) instancePodReady(
	ctx context.Context,
	spec VerifyClusterSpec,
	cluster OwnedCluster,
	timeout time.Duration,
) (bool, podIdentity, []model.EvidenceRecord, error) {
	args := c.args(spec, "get", "pod", spec.InstancePodName, "-o", "json")
	evidence, result, err := c.run(ctx, "kubectl-check-instance-ready", args, nil, timeout)
	if err != nil {
		return false, podIdentity{}, evidence, err
	}
	if !result.Evidence.ExitStatus.Success {
		return false, podIdentity{}, evidence, nil
	}
	ready, identity, err := podReady(result.Raw.Stdout, spec, cluster)
	if err != nil {
		return false, podIdentity{}, evidence, result.RedactError(err)
	}
	if err := rejectRedactedCanonicalFields(
		result,
		"CNPG instance observation",
		canonicalField{name: "pod_uid", value: identity.PodUID},
		canonicalField{name: "operator_version", value: identity.OperatorVersion},
	); err != nil {
		return false, podIdentity{}, evidence, err
	}
	return ready, identity, evidence, nil
}

func (c *KubectlClient) CaptureEvidence(ctx context.Context, spec VerifyClusterSpec, instance Instance, opts CaptureOptions) ([]model.EvidenceRecord, error) {
	type captureCommand struct {
		operation  string
		args       []string
		stdoutTail int
	}

	commands := []captureCommand{
		{
			operation: "kubectl-capture-cluster-yaml",
			args:      c.args(spec, "get", "cluster.postgresql.cnpg.io", spec.Name, "-o", "yaml"),
		},
		{
			operation: "kubectl-capture-pods",
			args:      c.args(spec, "get", "pods", "-l", "cnpg.io/cluster="+spec.Name, "-o", "wide"),
		},
		{
			operation: "kubectl-capture-instance-describe",
			args:      c.args(spec, "describe", "pod", spec.InstancePodName),
		},
		{
			operation: "kubectl-capture-pvcs",
			args:      c.args(spec, "get", "pvc", "-l", "cnpg.io/cluster="+spec.Name, "-o", "wide"),
		},
		{
			operation:  "kubectl-capture-events",
			args:       c.args(spec, "get", "events", "--sort-by=.metadata.creationTimestamp"),
			stdoutTail: opts.EventsTail,
		},
		{
			operation: "kubectl-capture-full-recovery-describe",
			args:      c.args(spec, "describe", "job/"+spec.FullRecoveryJob),
		},
		{
			operation: "kubectl-capture-full-recovery-log",
			args:      c.args(spec, append([]string{"logs", "job/" + spec.FullRecoveryJob, "--timestamps"}, tailArgs(opts.PostgresLogTail)...)...),
		},
		{
			operation: "kubectl-capture-full-recovery-bootstrap-log",
			args:      c.args(spec, append([]string{"logs", "job/" + spec.FullRecoveryJob, "-c", "bootstrap-controller", "--timestamps"}, tailArgs(opts.PostgresLogTail)...)...),
		},
	}
	if instance.PodName != "" {
		commands = append(commands,
			captureCommand{
				operation: "kubectl-capture-postgres-describe",
				args:      c.args(spec, "describe", "pod", instance.PodName),
			},
			captureCommand{
				operation: "kubectl-capture-postgres-log",
				args:      c.args(spec, append([]string{"logs", instance.PodName, "-c", "postgres", "--timestamps"}, tailArgs(opts.PostgresLogTail)...)...),
			},
			captureCommand{
				operation: "kubectl-capture-postgres-bootstrap-log",
				args:      c.args(spec, append([]string{"logs", instance.PodName, "-c", "bootstrap-controller", "--timestamps"}, tailArgs(opts.PostgresLogTail)...)...),
			},
		)
	}

	var evidence []model.EvidenceRecord
	var joined error
	for _, cmd := range commands {
		commandEvidence, err := c.bestEffortRun(ctx, cmd.operation, cmd.args)
		trimCommandEvidenceStdout(commandEvidence, cmd.stdoutTail)
		evidence = append(evidence, commandEvidence...)
		joined = errors.Join(joined, err)
	}

	evidence = append(evidence, c.captureSummaryEvidence(spec, opts, joined))
	return evidence, nil
}

func (c *KubectlClient) DeleteCluster(
	ctx context.Context,
	spec VerifyClusterSpec,
	owned OwnedCluster,
) ([]model.EvidenceRecord, error) {
	if !owned.Found {
		return nil, nil
	}
	if err := validateOwnershipID(spec.OwnershipID); err != nil {
		return nil, fmt.Errorf("delete CNPG cluster: %w", err)
	}
	if owned.Name != spec.Name {
		return nil, fmt.Errorf(
			"owned CNPG cluster %q does not match expected verify cluster %q",
			owned.Name,
			spec.Name,
		)
	}
	if err := validateOwnedCluster(spec, owned); err != nil {
		return nil, fmt.Errorf("delete CNPG cluster: %w", err)
	}
	if err := validateKubernetesUID(owned.UID); err != nil {
		return nil, fmt.Errorf("delete CNPG cluster: invalid observed uid: %w", err)
	}
	uri, err := c.resourceURI(spec, "cluster", owned.Name)
	if err != nil {
		return nil, err
	}
	payload, err := uidDeleteOptions(owned.UID)
	if err != nil {
		return nil, err
	}
	deleteEvidence, err := c.strictRun(
		ctx,
		"kubectl-delete-cluster",
		c.args(spec, "delete", "--raw="+uri, "-f", "-"),
		payload,
		c.deleteTimeout(),
	)
	if err != nil {
		return deleteEvidence, err
	}
	waitEvidence, err := c.strictRun(
		ctx,
		"kubectl-wait-cluster-delete",
		c.args(
			spec,
			"wait",
			"--for=delete",
			"cluster.postgresql.cnpg.io/"+owned.Name,
			"--timeout="+durationSeconds(c.deleteTimeout()),
		),
		nil,
		c.deleteTimeout(),
	)
	return append(deleteEvidence, waitEvidence...), err
}

func (c *KubectlClient) DeletePVCs(
	ctx context.Context,
	spec VerifyClusterSpec,
	cluster OwnedCluster,
	owned OwnedPVCs,
) ([]model.EvidenceRecord, error) {
	if _, err := ownershipSelector(spec, true); err != nil {
		return nil, err
	}
	if len(owned.Items) > maxOwnedPVCResources {
		return nil, fmt.Errorf("delete owned PVCs: resource count %d exceeds maximum %d", len(owned.Items), maxOwnedPVCResources)
	}
	evidence := make([]model.EvidenceRecord, 0, len(owned.Items)*2)
	for _, pvc := range owned.Items {
		if err := validateOwnedPVC(spec, cluster, pvc); err != nil {
			return evidence, err
		}
		uri, err := c.resourceURI(spec, "pvc", pvc.Name)
		if err != nil {
			return evidence, err
		}
		payload, err := uidDeleteOptions(pvc.UID)
		if err != nil {
			return evidence, err
		}
		deleteEvidence, err := c.strictRun(
			ctx,
			"kubectl-delete-pvc",
			c.args(spec, "delete", "--raw="+uri, "-f", "-"),
			payload,
			c.deleteTimeout(),
		)
		evidence = append(evidence, deleteEvidence...)
		if err != nil {
			return evidence, err
		}
		waitEvidence, err := c.strictRun(
			ctx,
			"kubectl-wait-pvc-delete",
			c.args(
				spec,
				"wait",
				"--for=delete",
				"persistentvolumeclaim/"+pvc.Name,
				"--timeout="+durationSeconds(c.deleteTimeout()),
			),
			nil,
			c.deleteTimeout(),
		)
		evidence = append(evidence, waitEvidence...)
		if err != nil {
			return evidence, err
		}
	}
	return evidence, nil
}

func validateOwnedPVC(spec VerifyClusterSpec, cluster OwnedCluster, pvc OwnedPVC) error {
	if err := validateKubernetesResourceName(pvc.Name); err != nil {
		return fmt.Errorf("delete owned PVC: invalid name: %w", err)
	}
	if err := validateKubernetesUID(pvc.UID); err != nil {
		return fmt.Errorf("delete owned PVC %q: invalid uid: %w", pvc.Name, err)
	}
	if pvc.OwnerClusterName != spec.Name {
		return fmt.Errorf(
			"delete owned PVC %q: owner cluster %q does not match %q",
			pvc.Name,
			pvc.OwnerClusterName,
			spec.Name,
		)
	}
	if err := validateKubernetesUID(pvc.OwnerClusterUID); err != nil {
		return fmt.Errorf("delete owned PVC %q: invalid owner uid: %w", pvc.Name, err)
	}
	if cluster.Found && pvc.OwnerClusterUID != cluster.UID {
		return fmt.Errorf(
			"delete owned PVC %q: owner uid does not match observed cluster uid",
			pvc.Name,
		)
	}
	contractDigest, err := spec.ContractDigest()
	if err != nil {
		return fmt.Errorf("delete owned PVC %q: %w", pvc.Name, err)
	}
	if pvc.ContractDigest != contractDigest {
		return fmt.Errorf(
			"delete owned PVC %q: recovery contract digest does not match",
			pvc.Name,
		)
	}
	return nil
}

func (c *KubectlClient) resourceURI(spec VerifyClusterSpec, resource, name string) (string, error) {
	namespace := firstNonEmpty(c.cfg.Namespace, spec.Namespace)
	if !isDNSSubdomain(namespace) || len(namespace) > 63 {
		return "", fmt.Errorf("delete CNPG resource: namespace %q is not a safe DNS label", namespace)
	}
	if err := validateKubernetesResourceName(name); err != nil {
		return "", fmt.Errorf("delete CNPG resource: %w", err)
	}
	switch resource {
	case "cluster":
		return "/apis/postgresql.cnpg.io/v1/namespaces/" + namespace + "/clusters/" + name, nil
	case "pvc":
		return "/api/v1/namespaces/" + namespace + "/persistentvolumeclaims/" + name, nil
	default:
		return "", fmt.Errorf("delete CNPG resource: unsupported resource %q", resource)
	}
}

func uidDeleteOptions(uid string) ([]byte, error) {
	if err := validateKubernetesUID(uid); err != nil {
		return nil, fmt.Errorf("encode Kubernetes DeleteOptions: %w", err)
	}
	payload, err := json.Marshal(struct {
		APIVersion        string `json:"apiVersion"`
		Kind              string `json:"kind"`
		PropagationPolicy string `json:"propagationPolicy"`
		Preconditions     struct {
			UID string `json:"uid"`
		} `json:"preconditions"`
	}{
		APIVersion:        "v1",
		Kind:              "DeleteOptions",
		PropagationPolicy: "Foreground",
		Preconditions: struct {
			UID string `json:"uid"`
		}{UID: uid},
	})
	if err != nil {
		return nil, fmt.Errorf("encode Kubernetes DeleteOptions: %w", err)
	}
	return payload, nil
}

func validateKubernetesResourceName(value string) error {
	if !isDNSSubdomain(value) || len(value) > 253 {
		return fmt.Errorf("resource name %q is not a safe DNS subdomain", value)
	}
	return nil
}

func isDNSSubdomain(value string) bool {
	if value == "" {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || sanitizeDNSLabel(label) != label {
			return false
		}
	}
	return true
}

func validateKubernetesUID(value string) error {
	if err := model.ValidateIdentity("uid", value); err != nil {
		return err
	}
	if len(value) > maxKubernetesUIDBytes {
		return fmt.Errorf("uid exceeds %d bytes", maxKubernetesUIDBytes)
	}
	return nil
}

func validateOwnedCluster(spec VerifyClusterSpec, owned OwnedCluster) error {
	if !owned.Found {
		return fmt.Errorf("owned CNPG cluster is not present")
	}
	if owned.Name != spec.Name {
		return fmt.Errorf(
			"owned CNPG cluster %q does not match expected verify cluster %q",
			owned.Name,
			spec.Name,
		)
	}
	if err := validateKubernetesUID(owned.UID); err != nil {
		return fmt.Errorf("owned CNPG cluster has invalid uid: %w", err)
	}
	contractDigest, err := spec.ContractDigest()
	if err != nil {
		return err
	}
	if owned.ContractDigest != contractDigest {
		return fmt.Errorf("owned CNPG cluster recovery contract digest does not match")
	}
	return nil
}

func ownershipSelector(spec VerifyClusterSpec, includeCluster bool) (string, error) {
	if err := validateOwnershipID(spec.OwnershipID); err != nil {
		return "", fmt.Errorf("cleanup: %w", err)
	}
	selector := labelOwnershipID + "=" + spec.OwnershipID
	if includeCluster {
		if spec.Name == "" || sanitizeDNSLabel(spec.Name) != spec.Name {
			return "", fmt.Errorf("cleanup: cnpg verify cluster name %q is not a safe label value", spec.Name)
		}
		selector = "cnpg.io/cluster=" + spec.Name + "," + selector
	}
	return selector, nil
}

func (c *KubectlClient) strictRun(ctx context.Context, operation string, args []string, stdin []byte, timeout time.Duration) ([]model.EvidenceRecord, error) {
	evidence, result, err := c.run(ctx, operation, args, stdin, timeout)
	if err != nil {
		return evidence, err
	}
	if !result.Evidence.ExitStatus.Success {
		return evidence, fmt.Errorf("%s failed: %s", operation, result.Evidence.ExitStatus.Summary())
	}
	return evidence, nil
}

func (c *KubectlClient) bestEffortRun(ctx context.Context, operation string, args []string) ([]model.EvidenceRecord, error) {
	evidence, result, err := c.run(ctx, operation, args, nil, c.cfg.Timeout)
	if err != nil {
		return evidence, err
	}
	if !result.Evidence.ExitStatus.Success {
		return evidence, fmt.Errorf("%s failed: %s", operation, result.Evidence.ExitStatus.Summary())
	}
	return evidence, nil
}

func (c *KubectlClient) run(ctx context.Context, operation string, args []string, stdin []byte, timeout time.Duration) ([]model.EvidenceRecord, command.Result, error) {
	result, err := c.runner.Run(ctx, command.Invocation{
		Path:         c.binary(),
		Args:         args,
		Stdin:        stdin,
		Timeout:      timeout,
		RedactValues: c.cfg.RedactValues,
	})
	result = result.WithRedactValues(c.cfg.RedactValues...)
	evidence := []model.EvidenceRecord{kubectlCommandEvidence(operation, result.Evidence)}
	if err != nil {
		return evidence, result, fmt.Errorf("%s: %w", operation, result.RedactError(err))
	}
	return evidence, result, nil
}

func (c *KubectlClient) args(spec VerifyClusterSpec, args ...string) []string {
	result := []string{}
	if c.cfg.Kubeconfig != "" {
		result = append(result, "--kubeconfig", c.cfg.Kubeconfig)
	}
	if c.cfg.Context != "" {
		result = append(result, "--context", c.cfg.Context)
	}
	namespace := firstNonEmpty(c.cfg.Namespace, spec.Namespace)
	if namespace != "" {
		result = append(result, "-n", namespace)
	}
	return append(result, args...)
}

func (c *KubectlClient) binary() string {
	if strings.TrimSpace(c.cfg.Binary) != "" {
		return strings.TrimSpace(c.cfg.Binary)
	}
	return defaultKubectlBinary
}

func (c *KubectlClient) deleteTimeout() time.Duration {
	if c.cfg.Timeout != 0 {
		return c.cfg.Timeout
	}
	return DefaultWaitTimeout
}

func kubectlCommandEvidence(operation string, evidence model.CommandEvidence) model.EvidenceRecord {
	collectedAt := evidence.FinishedAt
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}
	return model.EvidenceRecord{
		ID:          "cnpg:" + operation + ":" + collectedAt.Format(time.RFC3339Nano),
		Kind:        model.EvidenceCommand,
		Source:      string(model.RestoreTargetKubernetes),
		CollectedAt: collectedAt,
		Command:     &evidence,
		Attributes: map[string]string{
			"operation": operation,
		},
	}
}

func (c *KubectlClient) captureSummaryEvidence(spec VerifyClusterSpec, opts CaptureOptions, captureErr error) model.EvidenceRecord {
	now := time.Now().UTC()
	attributes := map[string]string{
		"cluster":        spec.Name,
		"namespace":      firstNonEmpty(c.cfg.Namespace, spec.Namespace),
		"operation":      "kubectl-capture-summary",
		"reason":         opts.Reason,
		"postgres_tail":  strconv.Itoa(opts.PostgresLogTail),
		"events_tail":    strconv.Itoa(opts.EventsTail),
		"best_effort":    "true",
		"capture_status": "passed",
	}
	if captureErr != nil {
		attributes["capture_status"] = "warning"
		attributes["capture_error"] = captureErr.Error()
	}
	return model.EvidenceRecord{
		ID:          "cnpg:kubectl-capture-summary:" + now.Format(time.RFC3339Nano),
		Kind:        model.EvidenceRuntime,
		Source:      string(model.RestoreTargetKubernetes),
		CollectedAt: now,
		Attributes:  attributes,
	}
}

func serviceHost(spec VerifyClusterSpec) string {
	host := spec.Name + "-rw"
	if spec.Namespace != "" {
		host += "." + spec.Namespace + ".svc"
	}
	return host
}

func durationSeconds(duration time.Duration) string {
	if duration <= 0 {
		return "0s"
	}
	return strconv.Itoa(int(duration.Round(time.Second).Seconds())) + "s"
}

func tailArgs(tail int) []string {
	if tail <= 0 {
		return nil
	}
	return []string{"--tail=" + strconv.Itoa(tail)}
}

func trimCommandEvidenceStdout(evidence []model.EvidenceRecord, maxLines int) {
	if maxLines <= 0 {
		return
	}
	for i := range evidence {
		commandEvidence := evidence[i].Command
		if commandEvidence == nil {
			continue
		}
		original := commandEvidence.Stdout
		trimmed := tailLines(original, maxLines)
		if trimmed != original {
			commandEvidence.Stdout = trimmed
			if commandEvidence.StdoutBytes == 0 {
				commandEvidence.StdoutBytes = int64(len(original))
			}
			commandEvidence.StdoutTruncated = true
		}
	}
}

func tailLines(value string, maxLines int) string {
	if maxLines <= 0 || value == "" {
		return value
	}
	hasFinalNewline := strings.HasSuffix(value, "\n")
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	if len(lines) <= maxLines {
		return value
	}
	result := strings.Join(lines[len(lines)-maxLines:], "\n")
	if hasFinalNewline {
		result += "\n"
	}
	return result
}

func fullRecoveryFailed(data []byte) (bool, string, error) {
	var list struct {
		Items *[]struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := jsonutil.DecodeOne(data, &list); err != nil {
		return false, "", fmt.Errorf("parse CNPG full-recovery pods: %w", err)
	}
	if list.Items == nil {
		return false, "", fmt.Errorf(
			"parse CNPG full-recovery pods: items array is missing or null",
		)
	}
	for _, item := range *list.Items {
		name := strings.TrimSpace(item.Metadata.Name)
		phase := strings.TrimSpace(item.Status.Phase)
		if name == "" {
			return false, "", fmt.Errorf("CNPG full-recovery pod has no metadata.name")
		}
		switch phase {
		case "Failed":
			return true, name, nil
		case "Pending", "Running", "Succeeded":
		case "Unknown":
			return false, "", fmt.Errorf(
				"CNPG full-recovery pod %q is in Unknown phase",
				name,
			)
		case "":
			return false, "", fmt.Errorf(
				"CNPG full-recovery pod %q has no status.phase",
				name,
			)
		default:
			return false, "", fmt.Errorf(
				"CNPG full-recovery pod %q has unsupported status.phase %q",
				name,
				phase,
			)
		}
	}
	return false, "", nil
}

type podIdentity struct {
	PodUID          string
	OperatorVersion string
}

func podReady(
	data []byte,
	spec VerifyClusterSpec,
	cluster OwnedCluster,
) (bool, podIdentity, error) {
	var pod struct {
		Metadata struct {
			Name            string            `json:"name"`
			UID             string            `json:"uid"`
			Labels          map[string]string `json:"labels"`
			Annotations     map[string]string `json:"annotations"`
			OwnerReferences []struct {
				APIVersion string `json:"apiVersion"`
				Kind       string `json:"kind"`
				Name       string `json:"name"`
				UID        string `json:"uid"`
			} `json:"ownerReferences"`
		} `json:"metadata"`
		Status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := jsonutil.DecodeOne(data, &pod); err != nil {
		return false, podIdentity{}, fmt.Errorf("parse CNPG instance pod: %w", err)
	}
	if err := validateOwnedCluster(spec, cluster); err != nil {
		return false, podIdentity{}, fmt.Errorf("validate CNPG instance cluster identity: %w", err)
	}
	name := strings.TrimSpace(pod.Metadata.Name)
	if name != spec.InstancePodName {
		return false, podIdentity{}, fmt.Errorf(
			"CNPG instance pod name %q does not match expected %q",
			name,
			spec.InstancePodName,
		)
	}
	uid := strings.TrimSpace(pod.Metadata.UID)
	if err := validateKubernetesUID(uid); err != nil {
		return false, podIdentity{}, fmt.Errorf("CNPG instance pod has invalid uid: %w", err)
	}
	if pod.Metadata.Labels["cnpg.io/cluster"] != spec.Name ||
		pod.Metadata.Labels[labelOwnershipID] != spec.OwnershipID {
		return false, podIdentity{}, fmt.Errorf(
			"CNPG instance pod labels do not match the owned recovery target",
		)
	}
	if pod.Metadata.Annotations[annotationRecoveryContract] != cluster.ContractDigest {
		return false, podIdentity{}, fmt.Errorf(
			"CNPG instance pod recovery contract digest does not match",
		)
	}
	ownerMatches := 0
	for _, owner := range pod.Metadata.OwnerReferences {
		if strings.TrimSpace(owner.APIVersion) == "postgresql.cnpg.io/v1" &&
			strings.TrimSpace(owner.Kind) == "Cluster" &&
			strings.TrimSpace(owner.Name) == cluster.Name &&
			strings.TrimSpace(owner.UID) == cluster.UID {
			ownerMatches++
		}
	}
	if ownerMatches != 1 {
		return false, podIdentity{}, fmt.Errorf(
			"CNPG instance pod must have exactly one ownerReference to cluster uid %q",
			cluster.UID,
		)
	}
	operatorVersion := strings.TrimSpace(pod.Metadata.Annotations["cnpg.io/operatorVersion"])
	identity := podIdentity{PodUID: uid, OperatorVersion: operatorVersion}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == "True" {
			return true, identity, nil
		}
	}
	return false, identity, nil
}
