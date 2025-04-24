/*
Copyright 2025 Rafal Jan.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	weightedv1alpha1 "github.com/rafal-jan/weighted-deployment-controller/api/v1alpha1"
)

// WeightedDeploymentReconciler reconciles a WeightedDeployment object
type WeightedDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Constants for labels
const (
	managedByLabel = "weighteddeployment.example.com/managed-by"
	targetLabel    = "weighteddeployment.example.com/target"
)

// Constants for controller behavior
const (
	defaultRequeueAfter = time.Second * 30 // Default requeue time
	maxRequeueBackoff   = time.Minute * 5  // Maximum backoff time
)

// +kubebuilder:rbac:groups=weighted.example.com,resources=weighteddeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=weighted.example.com,resources=weighteddeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=weighted.example.com,resources=weighteddeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
func (r *WeightedDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Reconciling WeightedDeployment")

	// Fetch the WeightedDeployment instance
	wd := &weightedv1alpha1.WeightedDeployment{}
	err := r.Get(ctx, req.NamespacedName, wd)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("WeightedDeployment resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - use exponential backoff
		log.Error(err, "Failed to get WeightedDeployment")
		// Calculate backoff duration based on generation to implement exponential backoff
		backoff := time.Second * time.Duration(math.Min(float64(wd.Generation*2), float64(maxRequeueBackoff.Seconds())))
		return ctrl.Result{RequeueAfter: backoff}, nil
	}

	// Calculate replica distribution
	targetReplicas := calculateReplicaDistribution(wd)

	// Reconcile underlying Deployments
	managedDeployments, err := r.reconcileDeployments(ctx, wd, targetReplicas)
	if err != nil {
		log.Error(err, "Failed to reconcile deployments")
		// Use exponential backoff for deployment reconciliation errors
		backoff := time.Second * time.Duration(math.Min(float64(wd.Generation*2), float64(maxRequeueBackoff.Seconds())))
		return ctrl.Result{RequeueAfter: backoff}, nil
	}

	// Cleanup orphaned Deployments
	err = r.cleanupOrphanedDeployments(ctx, wd, managedDeployments)
	if err != nil {
		log.Error(err, "Failed to cleanup orphaned deployments")
		backoff := time.Second * time.Duration(math.Min(float64(wd.Generation*2), float64(maxRequeueBackoff.Seconds())))
		return ctrl.Result{RequeueAfter: backoff}, nil
	}

	// Update status
	err = r.updateStatus(ctx, wd, managedDeployments)
	if err != nil {
		log.Error(err, "Failed to update status")
		backoff := time.Second * time.Duration(math.Min(float64(wd.Generation*2), float64(maxRequeueBackoff.Seconds())))
		return ctrl.Result{RequeueAfter: backoff}, nil
	}

	log.Info("Successfully reconciled WeightedDeployment")
	// Always requeue after the default period to ensure continuous reconciliation
	return ctrl.Result{RequeueAfter: defaultRequeueAfter}, nil
}

// calculateReplicaDistribution calculates the number of replicas for each target based on weights.
func calculateReplicaDistribution(wd *weightedv1alpha1.WeightedDeployment) map[string]int32 {
	targetReplicas := make(map[string]int32)
	totalWeight := int32(0)
	for _, target := range wd.Spec.Distribution.Targets {
		totalWeight += target.Weight
	}

	allocatedReplicas := int32(0)
	targetIndices := make([]int, len(wd.Spec.Distribution.Targets))
	for i := range wd.Spec.Distribution.Targets {
		targetIndices[i] = i
	}

	// Sort targets by weight descending to prioritize remainder distribution
	sort.SliceStable(targetIndices, func(i, j int) bool {
		return wd.Spec.Distribution.Targets[targetIndices[i]].Weight > wd.Spec.Distribution.Targets[targetIndices[j]].Weight
	})

	for _, idx := range targetIndices {
		target := wd.Spec.Distribution.Targets[idx]
		replicas := int32(math.Floor(float64(wd.Spec.Replicas) * float64(target.Weight) / float64(totalWeight)))
		targetReplicas[target.Name] = replicas
		allocatedReplicas += replicas
	}

	//TODO: RemainderDistributionType - Weight, Order
	// Distribute remainder replicas
	remainder := wd.Spec.Replicas - allocatedReplicas
	for i := 0; i < int(remainder); i++ {
		targetIdx := targetIndices[i%len(targetIndices)] // Distribute round-robin among sorted targets
		targetName := wd.Spec.Distribution.Targets[targetIdx].Name
		targetReplicas[targetName]++
	}

	return targetReplicas
}

// reconcileDeployments ensures the underlying Deployments match the desired state.
func (r *WeightedDeploymentReconciler) reconcileDeployments(ctx context.Context, wd *weightedv1alpha1.WeightedDeployment, targetReplicas map[string]int32) (map[string]*appsv1.Deployment, error) {
	log := log.FromContext(ctx)
	managedDeployments := make(map[string]*appsv1.Deployment)

	for _, target := range wd.Spec.Distribution.Targets {
		replicas := targetReplicas[target.Name]
		deploymentName := fmt.Sprintf("%s-%s", wd.Name, target.Name)
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deploymentName,
				Namespace: wd.Namespace,
			},
		}

		// Create or Update the Deployment
		result, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
			// Define the desired state of the Deployment
			desiredSpec := wd.Spec.Template.DeepCopy() // Start with the base template
			desiredSpec.Replicas = &replicas

			// Ensure selector is set if nil (it's required by Deployment)
			if desiredSpec.Selector == nil {
				desiredSpec.Selector = &metav1.LabelSelector{
					MatchLabels: make(map[string]string),
				}
			}
			// Ensure template labels are set if nil
			if desiredSpec.Template.ObjectMeta.Labels == nil {
				desiredSpec.Template.ObjectMeta.Labels = make(map[string]string)
			}

			// Add identifying labels to both selector and pod template
			commonLabels := map[string]string{
				managedByLabel: wd.Name,
				targetLabel:    target.Name,
			}
			for k, v := range commonLabels {
				desiredSpec.Selector.MatchLabels[k] = v
				desiredSpec.Template.ObjectMeta.Labels[k] = v
			}

			// Apply target-specific node affinity and tolerations
			if desiredSpec.Template.Spec.Affinity == nil {
				desiredSpec.Template.Spec.Affinity = &corev1.Affinity{}
			}
			if desiredSpec.Template.Spec.Affinity.NodeAffinity == nil {
				desiredSpec.Template.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
			}
			if desiredSpec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
				desiredSpec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{}
			}
			if len(desiredSpec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms) == 0 {
				desiredSpec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = append(
					desiredSpec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms,
					corev1.NodeSelectorTerm{},
				)
			}

			// Add target nodeSelector terms
			if len(target.NodeSelector) > 0 {
				matchExpressions := []corev1.NodeSelectorRequirement{}
				for k, v := range target.NodeSelector {
					matchExpressions = append(matchExpressions, corev1.NodeSelectorRequirement{
						Key:      k,
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{v},
					})
				}
				// Append to the first term (or create if none exists)
				// Note: This assumes a simple addition; more complex merging might be needed depending on desired behavior
				term := &desiredSpec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0]
				term.MatchExpressions = append(term.MatchExpressions, matchExpressions...)
			}

			// Apply target tolerations
			desiredSpec.Template.Spec.Tolerations = append(desiredSpec.Template.Spec.Tolerations, target.Tolerations...)

			// Set the desired spec
			deployment.Spec = *desiredSpec

			// Set owner reference
			if err := controllerutil.SetControllerReference(wd, deployment, r.Scheme); err != nil {
				log.Error(err, "Failed to set owner reference for Deployment", "Deployment.Name", deployment.Name)
				return err
			}

			// Add identifying labels to the Deployment itself
			if deployment.Labels == nil {
				deployment.Labels = make(map[string]string)
			}
			for k, v := range commonLabels {
				deployment.Labels[k] = v
			}

			return nil
		})

		if err != nil {
			log.Error(err, "Failed to create or update Deployment", "Deployment.Name", deployment.Name)
			return nil, err
		}

		if result == controllerutil.OperationResultCreated {
			log.Info("Created Deployment", "Deployment.Name", deployment.Name)
		} else if result == controllerutil.OperationResultUpdated {
			log.Info("Updated Deployment", "Deployment.Name", deployment.Name)
		} else {
			log.V(1).Info("Deployment unchanged", "Deployment.Name", deployment.Name) // Use V(1) for less verbose logging
		}

		managedDeployments[target.Name] = deployment
	}

	return managedDeployments, nil
}

// cleanupOrphanedDeployments deletes Deployments managed by this WeightedDeployment
// but whose corresponding target no longer exists in the spec.
func (r *WeightedDeploymentReconciler) cleanupOrphanedDeployments(ctx context.Context, wd *weightedv1alpha1.WeightedDeployment, currentManagedDeployments map[string]*appsv1.Deployment) error {
	log := log.FromContext(ctx)

	// List all Deployments managed by this WeightedDeployment instance
	deploymentList := &appsv1.DeploymentList{}
	listOpts := []client.ListOption{
		client.InNamespace(wd.Namespace),
		client.MatchingLabels{managedByLabel: wd.Name},
	}
	if err := r.List(ctx, deploymentList, listOpts...); err != nil {
		log.Error(err, "Failed to list managed deployments for cleanup")
		return err
	}

	// Create a set of current target names for quick lookup
	currentTargetNames := make(map[string]struct{})
	for _, target := range wd.Spec.Distribution.Targets {
		currentTargetNames[target.Name] = struct{}{}
	}

	// Identify and delete orphaned deployments
	for _, deployment := range deploymentList.Items {
		targetName, ok := deployment.Labels[targetLabel]
		if !ok {
			log.Info("Found managed deployment missing target label, skipping cleanup", "Deployment.Name", deployment.Name)
			continue // Should not happen if reconciliation works correctly
		}

		if _, exists := currentTargetNames[targetName]; !exists {
			log.Info("Deleting orphaned Deployment", "Deployment.Name", deployment.Name, "Target", targetName)
			if err := r.Delete(ctx, &deployment); err != nil {
				// Ignore not found errors, as it might have been deleted already
				if !apierrors.IsNotFound(err) {
					log.Error(err, "Failed to delete orphaned Deployment", "Deployment.Name", deployment.Name)
					return err
				}
			}
		}
	}

	return nil
}

// updateStatus updates the WeightedDeployment status based on the managed Deployments.
func (r *WeightedDeploymentReconciler) updateStatus(ctx context.Context, wd *weightedv1alpha1.WeightedDeployment, managedDeployments map[string]*appsv1.Deployment) error {
	log := log.FromContext(ctx)
	originalStatus := wd.Status.DeepCopy() // Keep original for comparison

	newStatus := weightedv1alpha1.WeightedDeploymentStatus{
		Replicas:           0,
		ReadyReplicas:      0,
		TargetStatus:       []weightedv1alpha1.TargetStatus{},
		ManagedDeployments: []weightedv1alpha1.ManagedDeployment{},
		Conditions:         wd.Status.Conditions, // Preserve existing conditions for now
	}

	// Fetch fresh status for all managed deployments
	deploymentList := &appsv1.DeploymentList{}
	listOpts := []client.ListOption{
		client.InNamespace(wd.Namespace),
		client.MatchingLabels{managedByLabel: wd.Name},
	}
	if err := r.List(ctx, deploymentList, listOpts...); err != nil {
		log.Error(err, "Failed to list managed deployments for status update")
		// Don't block status update entirely, maybe just log? Or update condition?
		// For now, proceed with potentially stale data from managedDeployments map
	} else {
		// Update managedDeployments map with fresh data
		freshManagedDeployments := make(map[string]*appsv1.Deployment)
		for i := range deploymentList.Items {
			dep := &deploymentList.Items[i] // Use pointer to item
			targetName := dep.Labels[targetLabel]
			if targetName != "" {
				freshManagedDeployments[targetName] = dep
			}
		}
		managedDeployments = freshManagedDeployments // Replace with fresh data
	}

	// Aggregate status from managed deployments
	for _, target := range wd.Spec.Distribution.Targets {
		ts := weightedv1alpha1.TargetStatus{Name: target.Name}
		md := weightedv1alpha1.ManagedDeployment{Name: fmt.Sprintf("%s-%s", wd.Name, target.Name)}

		deployment, exists := managedDeployments[target.Name]
		if exists {
			ts.Replicas = deployment.Status.Replicas
			ts.ReadyReplicas = deployment.Status.ReadyReplicas
			md.Replicas = deployment.Status.Replicas // Use status replicas

			newStatus.Replicas += deployment.Status.Replicas
			newStatus.ReadyReplicas += deployment.Status.ReadyReplicas
		} else {
			// Deployment might not exist yet or failed to list
			ts.Replicas = 0
			ts.ReadyReplicas = 0
			md.Replicas = 0
		}
		newStatus.TargetStatus = append(newStatus.TargetStatus, ts)
		newStatus.ManagedDeployments = append(newStatus.ManagedDeployments, md)
	}

	// Sort TargetStatus and ManagedDeployments for consistent ordering
	sort.Slice(newStatus.TargetStatus, func(i, j int) bool {
		return newStatus.TargetStatus[i].Name < newStatus.TargetStatus[j].Name
	})
	sort.Slice(newStatus.ManagedDeployments, func(i, j int) bool {
		return newStatus.ManagedDeployments[i].Name < newStatus.ManagedDeployments[j].Name
	})

	// TODO: Implement condition updates (e.g., Available, Progressing)

	// Only update if the status has actually changed
	// Note: Comparing complex structs directly can be tricky. A more robust comparison might be needed.
	// For simplicity, we compare key fields. A full comparison using reflect.DeepEqual might be better.
	statusChanged := newStatus.Replicas != originalStatus.Replicas ||
		newStatus.ReadyReplicas != originalStatus.ReadyReplicas ||
		len(newStatus.TargetStatus) != len(originalStatus.TargetStatus) // Basic length check

	// Add more detailed comparison if needed
	if !statusChanged && len(newStatus.TargetStatus) == len(originalStatus.TargetStatus) {
		// Compare TargetStatus content if lengths match
		for i := range newStatus.TargetStatus {
			if newStatus.TargetStatus[i] != originalStatus.TargetStatus[i] {
				statusChanged = true
				break
			}
		}
	}
	if !statusChanged && len(newStatus.ManagedDeployments) == len(originalStatus.ManagedDeployments) {
		// Compare ManagedDeployments content if lengths match
		for i := range newStatus.ManagedDeployments {
			if newStatus.ManagedDeployments[i] != originalStatus.ManagedDeployments[i] {
				statusChanged = true
				break
			}
		}
	}

	if statusChanged {
		wd.Status = newStatus
		log.Info("Updating WeightedDeployment status")
		if err := r.Status().Update(ctx, wd); err != nil {
			log.Error(err, "Failed to update WeightedDeployment status")
			return err
		}
	} else {
		log.V(1).Info("WeightedDeployment status unchanged")
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WeightedDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&weightedv1alpha1.WeightedDeployment{}).
		Owns(&appsv1.Deployment{}). // Watch Deployments owned by WeightedDeployment
		Named("weighteddeployment").
		Complete(r)
}
