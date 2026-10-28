package domain

import "strings"

// Component backup uses the existing immutable archive protocol with stack authority.
const (
	HostingDatabaseMySQLBackupComponentVersion    = "mysql.compose-logical-local.v1"
	HostingDatabaseMySQLBackupComponentCapability = "hosting_compose_mysql_backup_v1"
	HostingDatabaseBackupComponentVersion         = "postgresql.compose-logical-local.v1"
	HostingDatabaseBackupComponentCapability      = "hosting_compose_postgresql_backup_v1"
)

// HostingDatabaseBackupComponentTarget explicitly authorizes replacement of a separate database.
type HostingDatabaseBackupComponentTarget struct {
	ResourceID                  string `json:"resource_id"`
	Component                   string `json:"component"`
	ExpectedDeployedOperationID string `json:"expected_deployed_operation_id"`
	ExpectedResourceGeneration  int64  `json:"expected_resource_generation"`
	AcknowledgeReplacement      bool   `json:"acknowledge_replacement"`
}

// Validate requires an exact reviewed target and explicit destructive-import acknowledgement.
func (t HostingDatabaseBackupComponentTarget) Validate() error {
	if !validRuntimeReferenceID(t.ResourceID) || !stackName.MatchString(t.Component) || !validRuntimeReferenceID(t.ExpectedDeployedOperationID) || t.ExpectedResourceGeneration < 1 || !t.AcknowledgeReplacement {
		return ErrInvalidDatabaseBackup
	}
	return nil
}

// HostingDatabaseBackupComponents describes settled eligibility, never live health.
type HostingDatabaseBackupComponents struct {
	ResourceID         string                           `json:"resource_id"`
	ResourceGeneration int64                            `json:"resource_generation"`
	OperationID        string                           `json:"operation_id,omitempty"`
	Available          bool                             `json:"available"`
	Reason             string                           `json:"reason"`
	Components         []HostingDatabaseBackupComponent `json:"components"`
}

// HostingDatabaseBackupComponent exposes no Docker identity or guest environment.
type HostingDatabaseBackupComponent struct {
	Component    string `json:"component"`
	Engine       string `json:"engine"`
	MajorVersion int    `json:"major_version"`
	Running      bool   `json:"running"`
	Eligible     bool   `json:"eligible"`
	Reason       string `json:"reason"`
}

// HostingDatabaseBackupComponentPlan selects components from the immutable stack receipts.
type HostingDatabaseBackupComponentPlan struct {
	Source string `json:"source"`
	Target string `json:"target,omitempty"`
}

func (o HostingDatabaseBackupOperation) validateComponentIdentity() error {
	if o.Version == HostingDatabaseMySQLBackupComponentVersion && o.RemoteTarget != nil {
		return ErrInvalidDatabaseBackup
	}
	if o.Version == HostingDatabaseBackupVersion || o.Version == HostingDatabaseMySQLBackupVersion {
		if o.SourceComponent != "" || o.SourceDeployedOperationID != "" || o.TargetComponent != "" || o.TargetDeployedOperationID != "" {
			return ErrInvalidDatabaseBackup
		}
		return nil
	}
	if !IsHostingDatabaseBackupComponentVersion(o.Version) || !stackName.MatchString(o.SourceComponent) || !validRuntimeReferenceID(o.SourceDeployedOperationID) {
		return ErrInvalidDatabaseBackup
	}
	if o.Mode == "restore" {
		if !stackName.MatchString(o.TargetComponent) || !validRuntimeReferenceID(o.TargetDeployedOperationID) {
			return ErrInvalidDatabaseBackup
		}
	} else if o.TargetComponent != "" || o.TargetDeployedOperationID != "" {
		return ErrInvalidDatabaseBackup
	}
	return nil
}

// IsHostingDatabaseBackupComponentVersion recognizes only implemented component adapters.
func IsHostingDatabaseBackupComponentVersion(version string) bool {
	return version == HostingDatabaseBackupComponentVersion || version == HostingDatabaseMySQLBackupComponentVersion
}

// HostingDatabaseBackupComponentVersionFor derives the protocol from the immutable service profile.
func HostingDatabaseBackupComponentVersionFor(h HostingHealthy, p HostingApplyPlan, name string) string {
	if _, err := HostingPostgresComponent(h, p, name); err == nil {
		return HostingDatabaseBackupComponentVersion
	}
	if _, err := HostingMySQLComponent(h, p, name); err == nil {
		return HostingDatabaseMySQLBackupComponentVersion
	}
	return ""
}

// HostingDatabaseBackupComponentFor verifies the selected engine without falling back to another adapter.
func HostingDatabaseBackupComponentFor(version string, h HostingHealthy, p HostingApplyPlan, name string) (HostingStackComponent, error) {
	switch version {
	case HostingDatabaseBackupComponentVersion:
		return HostingPostgresComponent(h, p, name)
	case HostingDatabaseMySQLBackupComponentVersion:
		return HostingMySQLComponent(h, p, name)
	}
	return HostingStackComponent{}, ErrInvalidDatabaseBackup
}

// HostingMySQLComponent verifies the pinned official WordPress database profile.
// Guest credentials remain inside the inspected container; no password is returned.
func HostingMySQLComponent(healthy HostingHealthy, plan HostingApplyPlan, name string) (HostingStackComponent, error) {
	if _, err := CompileHostingStackStopPlan(healthy, plan); err != nil || name != "mysql" || name == plan.Stack.Definition.PublicComponent {
		return HostingStackComponent{}, ErrInvalidDatabaseBackup
	}
	var service *HostingStackService
	for i := range plan.Stack.Definition.Services {
		if plan.Stack.Definition.Services[i].Name == name {
			service = &plan.Stack.Definition.Services[i]
		}
	}
	if service == nil || service.Command != nil || len(service.Mounts) != 1 || service.Mounts[0] != (HostingStackMount{Volume: "mysql-data", Target: "/var/lib/mysql"}) || service.Environment["MYSQL_DATABASE"] != "wordpress" || service.Environment["MYSQL_ROOT_PASSWORD"] != "${SERVICE_PASSWORD_ROOT:?Supply a separate managed MySQL root password reference}" {
		return HostingStackComponent{}, ErrInvalidDatabaseBackup
	}
	for _, other := range plan.Stack.Definition.Services {
		if other.Name != name {
			for _, mount := range other.Mounts {
				if mount.Volume == "mysql-data" {
					return HostingStackComponent{}, ErrInvalidDatabaseBackup
				}
			}
		}
	}
	qualified := false
	for _, image := range plan.Stack.Images {
		if image.Component == name {
			qualified = image.Release.CanonicalImage == "docker.io/library/mysql@sha256:a0225d1ea6631c8bc2385dc4c2faba17bbf1834ecf369e1659a9a382bd8d5dd7"
		}
	}
	if !qualified {
		return HostingStackComponent{}, ErrInvalidDatabaseBackup
	}
	for _, component := range healthy.Stack.Components {
		if component.Component == name && component.Running {
			return component, nil
		}
	}
	return HostingStackComponent{}, ErrInvalidDatabaseBackup
}

// HostingPostgresComponent verifies the qualified official PostgreSQL 16 profile.
// The runner additionally verifies the live container, image, mounts and guest tools.
func HostingPostgresComponent(healthy HostingHealthy, plan HostingApplyPlan, name string) (HostingStackComponent, error) {
	if _, err := CompileHostingStackStopPlan(healthy, plan); err != nil || !stackName.MatchString(name) || name == plan.Stack.Definition.PublicComponent {
		return HostingStackComponent{}, ErrInvalidDatabaseBackup
	}
	var service *HostingStackService
	for i := range plan.Stack.Definition.Services {
		if plan.Stack.Definition.Services[i].Name == name {
			service = &plan.Stack.Definition.Services[i]
		}
	}
	if service == nil || service.Command != nil || len(service.Mounts) != 1 || service.Mounts[0].ReadOnly || service.Mounts[0].Target != "/var/lib/postgresql/data" {
		return HostingStackComponent{}, ErrInvalidDatabaseBackup
	}
	for _, other := range plan.Stack.Definition.Services {
		if other.Name != name {
			for _, mount := range other.Mounts {
				if mount.Volume == service.Mounts[0].Volume {
					return HostingStackComponent{}, ErrInvalidDatabaseBackup
				}
			}
		}
	}
	qualified := false
	for _, image := range plan.Stack.Images {
		if image.Component == name {
			qualified = strings.HasPrefix(image.Release.CanonicalImage, "docker.io/library/postgres@sha256:")
		}
	}
	if !qualified {
		return HostingStackComponent{}, ErrInvalidDatabaseBackup
	}
	// A digest alone does not identify the server major; the runner verifies version 16.
	for _, component := range healthy.Stack.Components {
		if component.Component == name && component.Running {
			return component, nil
		}
	}
	return HostingStackComponent{}, ErrInvalidDatabaseBackup
}

func (p HostingDatabaseBackupPlan) validateComponent() error {
	hash := p.Hash
	p.Hash = ""
	if hash != HostingDocumentHash(p) || p.Component == nil || p.Operation.validateComponentIdentity() != nil || !validRuntimeReferenceID(p.Operation.ID) || !validRuntimeReferenceID(p.ActorID) || p.RunnerID != p.Source.Snapshot.RunnerID || p.Operation.SourceResourceID != p.Source.Snapshot.ResourceID || p.Operation.SourceGeneration != p.Source.Snapshot.ResourceGeneration || p.Component.Source != p.Operation.SourceComponent || p.Operation.SourceDeployedOperationID != p.Source.OperationID || p.SourceStorage != (HostingStorage{}) || len(p.Credentials) != 0 || p.TargetStorage != nil || p.MaxBytes < 5 || p.MaxBytes > HostingDatabaseBackupMaxBytes || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 3600 || p.Operation.MaxBytes != p.MaxBytes || p.Operation.TimeoutSeconds != p.TimeoutSeconds || p.LocationID != p.Operation.BackupID {
		return ErrInvalidDatabaseBackup
	}
	if _, err := HostingDatabaseBackupComponentFor(p.Operation.Version, p.Source, p.SourcePlan, p.Component.Source); err != nil {
		return err
	}
	if p.Operation.Mode == "backup" {
		if p.Target != nil || p.TargetPlan != nil || p.Component.Target != "" || p.Operation.TargetResourceID != "" || p.Operation.BackupID != p.Operation.ID {
			return ErrInvalidDatabaseBackup
		}
		return nil
	}
	if p.Operation.Mode != "restore" || p.Target == nil || p.TargetPlan == nil || p.Component.Target != p.Operation.TargetComponent || p.Operation.TargetDeployedOperationID != p.Target.OperationID || p.Operation.TargetResourceID != p.Target.Snapshot.ResourceID || p.Target.Snapshot.ResourceID == p.Source.Snapshot.ResourceID || p.Target.Snapshot.DestinationID != p.Source.Snapshot.DestinationID || p.Target.Snapshot.BindingID == p.Source.Snapshot.BindingID || p.Target.Snapshot.RunnerID != p.RunnerID || p.Target.Release.DaemonID != p.Source.Release.DaemonID || p.Operation.Blob == nil || p.Operation.Blob.Validate() != nil || p.Operation.BackupID == p.Operation.ID {
		return ErrInvalidDatabaseBackup
	}
	_, err := HostingDatabaseBackupComponentFor(p.Operation.Version, *p.Target, *p.TargetPlan, p.Component.Target)
	if err != nil {
		return err
	}
	if p.Source.OperationID == p.Target.OperationID || p.Source.Stack.NetworkID == p.Target.Stack.NetworkID || p.SourcePlan.Stack.NetworkName == p.TargetPlan.Stack.NetworkName {
		return ErrInvalidDatabaseBackup
	}
	for _, source := range p.Source.Stack.Components {
		for _, target := range p.Target.Stack.Components {
			if source.ContainerID == target.ContainerID {
				return ErrInvalidDatabaseBackup
			}
		}
	}
	for _, source := range p.Source.Stack.Volumes {
		for _, target := range p.Target.Stack.Volumes {
			if source.VolumeName == target.VolumeName {
				return ErrInvalidDatabaseBackup
			}
		}
	}
	return err
}

// ValidateComponentMutation binds actual writer observations to the frozen target.
func (p HostingDatabaseBackupPlan) ValidateComponentMutation(in HostingDatabaseBackupMutation) error {
	if p.Component == nil {
		if in.ComponentVersion != "" {
			return ErrInvalidDatabaseBackup
		}
		return nil
	}
	if in.ComponentVersion != p.Operation.Version {
		return ErrInvalidDatabaseBackup
	}
	if p.Operation.Mode != "restore" || in.Phase == "failed" {
		if len(in.Writers) != 0 {
			return ErrInvalidDatabaseBackup
		}
		return nil
	}
	if p.Target == nil || len(in.Writers) != len(p.Target.Stack.Components)-1 {
		return ErrInvalidDatabaseBackup
	}
	seen := map[string]bool{}
	for _, writer := range in.Writers {
		matched := false
		for _, c := range p.Target.Stack.Components {
			if c.Component != p.Component.Target && c.ContainerID == writer.ContainerID && c.OperationID == writer.OperationID && c.ImageID == writer.ImageID && writer.PlanHash == p.Target.PlanHash && writer.ReleaseHash == p.Target.Release.Hash && !seen[c.ContainerID] {
				matched = true
				seen[c.ContainerID] = true
			}
		}
		if !matched {
			return ErrInvalidDatabaseBackup
		}
	}
	return nil
}
