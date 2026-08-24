package argocd

type ApplicationList struct {
	Items []Application `json:"items"`
}

type Application struct {
	Metadata Metadata `json:"metadata"`
	Spec     Spec     `json:"spec"`
	Status   Status   `json:"status"`
}

type Metadata struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
}

type Spec struct {
	Source Source `json:"source"`
}

type Source struct {
	Path           string `json:"path"`
	TargetRevision string `json:"targetRevision"`
	RepoURL        string `json:"repoURL"`
}

type Status struct {
	Health         Health         `json:"health"`
	Sync           Sync           `json:"sync"`
	OperationState OperationState `json:"operationState"`
	History        []HistoryEntry `json:"history"`
	Resources      []Resource     `json:"resources"`
	Summary        Summary        `json:"summary"`
}

type Health struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Revision is null on Applications that have never completed a sync.
type Sync struct {
	Status   string `json:"status"`
	Revision string `json:"revision"`
}

type OperationState struct {
	Phase      string `json:"phase"`
	Message    string `json:"message"`
	FinishedAt string `json:"finishedAt"`
}

type HistoryEntry struct {
	ID         int    `json:"id"`
	Revision   string `json:"revision"`
	DeployedAt string `json:"deployedAt"`
}

type Resource struct {
	Group           string `json:"group"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	Status          string `json:"status"`
	RequiresPruning bool   `json:"requiresPruning"`
}

// Summary carries the container images ArgoCD observed for the application.
type Summary struct {
	Images []string `json:"images"`
}
