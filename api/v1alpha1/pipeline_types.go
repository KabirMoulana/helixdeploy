package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// +groupName=helix.io

// Pipeline is the Schema for the pipelines API.
// It represents a full CI/CD pipeline definition as a Kubernetes-native resource.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=pl
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Current Stage",type=string,JSONPath=`.status.currentStage`
// +kubebuilder:printcolumn:name="Last Run",type=date,JSONPath=`.status.lastRunTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Pipeline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PipelineSpec   `json:"spec,omitempty"`
	Status PipelineStatus `json:"status,omitempty"`
}

type PipelineSpec struct {
	// Description is a human-readable description of what this pipeline does.
	// +optional
	Description string `json:"description,omitempty"`

	// Triggers defines what events start this pipeline.
	Triggers []Trigger `json:"triggers"`

	// Stages defines the ordered list of stages in this pipeline.
	// Stages run sequentially; steps within a stage run in parallel by default.
	Stages []Stage `json:"stages"`

	// Timeout is the maximum duration for the entire pipeline run.
	// +kubebuilder:default="1h"
	Timeout string `json:"timeout,omitempty"`

	// Notifications configures where pipeline events are sent.
	// +optional
	Notifications *NotificationConfig `json:"notifications,omitempty"`

	// ServiceAccountName to run pipeline tasks under.
	// +kubebuilder:default="helix-pipeline-runner"
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

type Trigger struct {
	// Type is the trigger type: "push", "pull_request", "schedule", "manual"
	// +kubebuilder:validation:Enum=push;pull_request;schedule;manual
	Type string `json:"type"`

	// Branch filters (for push/pr triggers)
	// +optional
	Branches []string `json:"branches,omitempty"`

	// Cron expression (for schedule triggers)
	// +optional
	Cron string `json:"cron,omitempty"`
}

type Stage struct {
	// Name is a unique identifier for this stage within the pipeline.
	Name string `json:"name"`

	// Steps are the individual tasks in this stage.
	Steps []Step `json:"steps"`

	// ApprovalRequired pauses the pipeline and waits for manual approval before proceeding.
	// +optional
	ApprovalRequired bool `json:"approvalRequired,omitempty"`

	// ApprovalTimeout is how long to wait for approval before auto-failing.
	// +optional
	ApprovalTimeout string `json:"approvalTimeout,omitempty"`

	// Condition is a CEL expression that must be true for this stage to run.
	// Example: "stages.build.result == 'success' && branch == 'main'"
	// +optional
	Condition string `json:"condition,omitempty"`
}

type Step struct {
	// Name is the step identifier.
	Name string `json:"name"`

	// Image is the container image to run this step in.
	Image string `json:"image"`

	// Script is the shell script to execute.
	// +optional
	Script string `json:"script,omitempty"`

	// Command overrides the image entrypoint.
	// +optional
	Command []string `json:"command,omitempty"`

	// Env are additional environment variables.
	// +optional
	Env []EnvVar `json:"env,omitempty"`

	// Resources defines CPU/memory for the step container.
	// +optional
	Resources *StepResources `json:"resources,omitempty"`

	// Cache configures step-level caching (e.g. go mod cache, npm cache).
	// +optional
	Cache *CacheConfig `json:"cache,omitempty"`
}

type EnvVar struct {
	Name string `json:"name"`
	// +optional
	Value string `json:"value,omitempty"`
	// +optional
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

type SecretRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type StepResources struct {
	CPURequest    string `json:"cpuRequest,omitempty"`
	MemoryRequest string `json:"memoryRequest,omitempty"`
	CPULimit      string `json:"cpuLimit,omitempty"`
	MemoryLimit   string `json:"memoryLimit,omitempty"`
}

type CacheConfig struct {
	// Key is the cache key template (supports Go template syntax with git SHA, branch, etc.)
	Key  string   `json:"key"`
	Path []string `json:"path"`
}

type NotificationConfig struct {
	// SlackChannel to post pipeline events to.
	// +optional
	SlackChannel string `json:"slackChannel,omitempty"`

	// WebhookURL for generic HTTP notifications.
	// +optional
	WebhookURL string `json:"webhookUrl,omitempty"`

	// OnEvents limits notifications to specific events. Default: all events.
	// +optional
	OnEvents []string `json:"onEvents,omitempty"`
}

// PipelineStatus defines the observed state of Pipeline.
type PipelineStatus struct {
	// Phase is the overall pipeline phase: Pending, Running, Succeeded, Failed, Cancelled
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Cancelled;AwaitingApproval
	Phase string `json:"phase,omitempty"`

	// CurrentStage is the name of the currently executing stage.
	CurrentStage string `json:"currentStage,omitempty"`

	// RunID is the unique identifier for the current pipeline run.
	RunID string `json:"runId,omitempty"`

	// LastRunTime is when the pipeline last started.
	// +optional
	LastRunTime *metav1.Time `json:"lastRunTime,omitempty"`

	// LastRunDuration is how long the last run took.
	LastRunDuration string `json:"lastRunDuration,omitempty"`

	// StageResults tracks the result of each completed stage.
	StageResults map[string]StageResult `json:"stageResults,omitempty"`

	// Conditions is a standard Kubernetes condition list.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type StageResult struct {
	// Result: "success", "failure", "skipped"
	Result    string       `json:"result"`
	StartTime *metav1.Time `json:"startTime,omitempty"`
	EndTime   *metav1.Time `json:"endTime,omitempty"`
	Message   string       `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
type PipelineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Pipeline `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Pipeline{}, &PipelineList{})
}

// SchemeBuilder is used to add functions to the scheme.
var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(
		GroupVersion,
		&Pipeline{},
		&PipelineList{},
		&Release{},
		&ReleaseList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
