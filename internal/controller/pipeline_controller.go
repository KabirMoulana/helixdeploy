package controller

import (
	"context"
	"fmt"
	"time"

	helixv1 "github.com/KabirMoulana/helixdeploy/api/v1alpha1"
	"github.com/KabirMoulana/helixdeploy/internal/tekton"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	pipelineFinalizer = "helix.io/pipeline-cleanup"

	phaseRunning         = "Running"
	phasePending         = "Pending"
	phaseSucceeded       = "Succeeded"
	phaseFailed          = "Failed"
	phaseAwaitingApproval = "AwaitingApproval"
)

// PipelineReconciler reconciles Pipeline objects.
// It follows the standard controller-runtime reconcile loop pattern:
//   1. Fetch the Pipeline object
//   2. Handle deletion (finalizer cleanup)
//   3. Ensure finalizer is registered
//   4. Reconcile desired state → actual state
//
// +kubebuilder:rbac:groups=helix.io,resources=pipelines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=helix.io,resources=pipelines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=helix.io,resources=pipelines/finalizers,verbs=update
// +kubebuilder:rbac:groups=tekton.dev,resources=pipelineruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
type PipelineReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Log          *zap.Logger
	TektonClient *tekton.Client
}

func (r *PipelineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.With(zap.String("pipeline", req.NamespacedName.String()))

	// ── 1. Fetch the Pipeline ─────────────────────────────────────────────────
	pipeline := &helixv1.Pipeline{}
	if err := r.Get(ctx, req.NamespacedName, pipeline); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil // Deleted — nothing to do
		}
		return ctrl.Result{}, fmt.Errorf("failed to get pipeline: %w", err)
	}

	// ── 2. Handle deletion ────────────────────────────────────────────────────
	if !pipeline.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(pipeline, pipelineFinalizer) {
			if err := r.cleanup(ctx, pipeline); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(pipeline, pipelineFinalizer)
			return ctrl.Result{}, r.Update(ctx, pipeline)
		}
		return ctrl.Result{}, nil
	}

	// ── 3. Register finalizer ─────────────────────────────────────────────────
	if !controllerutil.ContainsFinalizer(pipeline, pipelineFinalizer) {
		controllerutil.AddFinalizer(pipeline, pipelineFinalizer)
		if err := r.Update(ctx, pipeline); err != nil {
			return ctrl.Result{}, err
		}
	}

	// ── 4. Reconcile ──────────────────────────────────────────────────────────
	return r.reconcilePipeline(ctx, pipeline, log)
}

func (r *PipelineReconciler) reconcilePipeline(
	ctx context.Context,
	pipeline *helixv1.Pipeline,
	log *zap.Logger,
) (ctrl.Result, error) {

	switch pipeline.Status.Phase {
	case "", phasePending:
		return r.startPipeline(ctx, pipeline, log)

	case phaseRunning:
		return r.syncRunningPipeline(ctx, pipeline, log)

	case phaseAwaitingApproval:
		return r.checkApproval(ctx, pipeline, log)

	case phaseSucceeded, phaseFailed:
		// Terminal state — nothing more to reconcile unless spec changes
		log.Debug("pipeline in terminal state", zap.String("phase", pipeline.Status.Phase))
		return ctrl.Result{}, nil

	default:
		log.Warn("unknown pipeline phase", zap.String("phase", pipeline.Status.Phase))
		return ctrl.Result{}, nil
	}
}

// startPipeline creates the Tekton PipelineRun for the first pending stage.
func (r *PipelineReconciler) startPipeline(
	ctx context.Context,
	pipeline *helixv1.Pipeline,
	log *zap.Logger,
) (ctrl.Result, error) {
	log.Info("starting pipeline", zap.String("runId", pipeline.Status.RunID))

	if len(pipeline.Spec.Stages) == 0 {
		return ctrl.Result{}, r.setPhase(ctx, pipeline, phaseFailed, "no stages defined")
	}

	firstStage := pipeline.Spec.Stages[0]

	// Create Tekton PipelineRun for first stage
	runID := generateRunID(pipeline)
	pipelineRun, err := r.TektonClient.CreatePipelineRun(ctx, pipeline, firstStage, runID)
	if err != nil {
		return ctrl.Result{RequeueAfter: 15 * time.Second},
			fmt.Errorf("failed to create PipelineRun: %w", err)
	}

	log.Info("created Tekton PipelineRun",
		zap.String("pipelineRun", pipelineRun.Name),
		zap.String("stage", firstStage.Name),
	)

	patch := client.MergeFrom(pipeline.DeepCopy())
	pipeline.Status.Phase = phaseRunning
	pipeline.Status.CurrentStage = firstStage.Name
	pipeline.Status.RunID = runID
	pipeline.Status.LastRunTime = &metav1.Time{Time: time.Now()}
	if pipeline.Status.StageResults == nil {
		pipeline.Status.StageResults = make(map[string]helixv1.StageResult)
	}

	if err := r.Status().Patch(ctx, pipeline, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch status: %w", err)
	}

	// Requeue to check status
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// syncRunningPipeline checks the active Tekton PipelineRun and advances stages.
func (r *PipelineReconciler) syncRunningPipeline(
	ctx context.Context,
	pipeline *helixv1.Pipeline,
	log *zap.Logger,
) (ctrl.Result, error) {

	// Fetch current Tekton PipelineRun status
	status, err := r.TektonClient.GetPipelineRunStatus(ctx, pipeline.Namespace, pipeline.Status.RunID, pipeline.Status.CurrentStage)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	}

	log.Debug("tekton pipelinerun status",
		zap.String("stage", pipeline.Status.CurrentStage),
		zap.String("state", status.State),
	)

	switch status.State {
	case "Running", "Pending":
		// Still in progress — check again soon
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil

	case "Succeeded":
		return r.advanceToNextStage(ctx, pipeline, log)

	case "Failed":
		patch := client.MergeFrom(pipeline.DeepCopy())
		pipeline.Status.Phase = phaseFailed
		pipeline.Status.StageResults[pipeline.Status.CurrentStage] = helixv1.StageResult{
			Result:  "failure",
			Message: status.Message,
			EndTime: &metav1.Time{Time: time.Now()},
		}
		r.recordEvent(pipeline, corev1.EventTypeWarning, "StageFailed",
			fmt.Sprintf("Stage %q failed: %s", pipeline.Status.CurrentStage, status.Message))
		return ctrl.Result{}, r.Status().Patch(ctx, pipeline, patch)

	default:
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
}

// advanceToNextStage moves the pipeline to the next stage, handling approvals.
func (r *PipelineReconciler) advanceToNextStage(
	ctx context.Context,
	pipeline *helixv1.Pipeline,
	log *zap.Logger,
) (ctrl.Result, error) {

	// Mark current stage as succeeded
	patch := client.MergeFrom(pipeline.DeepCopy())
	pipeline.Status.StageResults[pipeline.Status.CurrentStage] = helixv1.StageResult{
		Result:  "success",
		EndTime: &metav1.Time{Time: time.Now()},
	}

	// Find next stage
	nextStage := r.findNextStage(pipeline)
	if nextStage == nil {
		// All stages complete
		pipeline.Status.Phase = phaseSucceeded
		duration := time.Since(pipeline.Status.LastRunTime.Time)
		pipeline.Status.LastRunDuration = duration.Round(time.Second).String()
		log.Info("pipeline succeeded", zap.String("duration", pipeline.Status.LastRunDuration))
		r.recordEvent(pipeline, corev1.EventTypeNormal, "PipelineSucceeded", "All stages completed successfully")
		return ctrl.Result{}, r.Status().Patch(ctx, pipeline, patch)
	}

	// Check if next stage requires approval
	if nextStage.ApprovalRequired {
		pipeline.Status.Phase = phaseAwaitingApproval
		pipeline.Status.CurrentStage = nextStage.Name
		log.Info("waiting for manual approval", zap.String("stage", nextStage.Name))
		r.recordEvent(pipeline, corev1.EventTypeNormal, "AwaitingApproval",
			fmt.Sprintf("Stage %q requires manual approval", nextStage.Name))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.Status().Patch(ctx, pipeline, patch)
	}

	// Start next stage
	pipeline.Status.CurrentStage = nextStage.Name
	pipeline.Status.Phase = phaseRunning
	if err := r.Status().Patch(ctx, pipeline, patch); err != nil {
		return ctrl.Result{}, err
	}

	_, err := r.TektonClient.CreatePipelineRun(ctx, pipeline, *nextStage, pipeline.Status.RunID)
	if err != nil {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, err
	}

	log.Info("advanced to next stage", zap.String("stage", nextStage.Name))
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *PipelineReconciler) checkApproval(
	ctx context.Context,
	pipeline *helixv1.Pipeline,
	log *zap.Logger,
) (ctrl.Result, error) {
	// Approval is granted by patching an annotation on the Pipeline:
	// kubectl annotate pipeline my-pipeline helix.io/approve=<runID>
	approvalAnnotation := pipeline.Annotations["helix.io/approve"]
	if approvalAnnotation == pipeline.Status.RunID {
		log.Info("approval received", zap.String("stage", pipeline.Status.CurrentStage))
		pipeline.Status.Phase = phaseRunning
		if err := r.Status().Update(ctx, pipeline); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Check timeout
	currentStageSpec := r.findStageByName(pipeline, pipeline.Status.CurrentStage)
	if currentStageSpec != nil && currentStageSpec.ApprovalTimeout != "" {
		timeout, err := time.ParseDuration(currentStageSpec.ApprovalTimeout)
		if err == nil && time.Since(pipeline.Status.LastRunTime.Time) > timeout {
			log.Warn("approval timeout reached", zap.String("stage", pipeline.Status.CurrentStage))
			return ctrl.Result{}, r.setPhase(ctx, pipeline, phaseFailed, "approval timeout")
		}
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *PipelineReconciler) cleanup(ctx context.Context, pipeline *helixv1.Pipeline) error {
	// Cancel any running Tekton PipelineRuns for this pipeline
	return r.TektonClient.CancelAllRuns(ctx, pipeline.Namespace, pipeline.Name)
}

func (r *PipelineReconciler) findNextStage(pipeline *helixv1.Pipeline) *helixv1.Stage {
	currentFound := false
	for i := range pipeline.Spec.Stages {
		if currentFound {
			return &pipeline.Spec.Stages[i]
		}
		if pipeline.Spec.Stages[i].Name == pipeline.Status.CurrentStage {
			currentFound = true
		}
	}
	return nil
}

func (r *PipelineReconciler) findStageByName(pipeline *helixv1.Pipeline, name string) *helixv1.Stage {
	for i := range pipeline.Spec.Stages {
		if pipeline.Spec.Stages[i].Name == name {
			return &pipeline.Spec.Stages[i]
		}
	}
	return nil
}

func (r *PipelineReconciler) setPhase(ctx context.Context, pipeline *helixv1.Pipeline, phase, msg string) error {
	patch := client.MergeFrom(pipeline.DeepCopy())
	pipeline.Status.Phase = phase
	if msg != "" {
		r.recordEvent(pipeline, corev1.EventTypeWarning, "PhaseChanged", msg)
	}
	return r.Status().Patch(ctx, pipeline, patch)
}

func (r *PipelineReconciler) recordEvent(pipeline *helixv1.Pipeline, eventType, reason, msg string) {
	// Uses controller-runtime's event recorder — omitted for brevity
}

func generateRunID(pipeline *helixv1.Pipeline) string {
	return fmt.Sprintf("%s-%d", pipeline.Name, time.Now().UnixMilli())
}

// SetupWithManager registers the controller with the manager.
func (r *PipelineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&helixv1.Pipeline{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
