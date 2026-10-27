package domain

import "testing"

func TestCatalogRequirementsPreservePinnedTemplates(t *testing.T) {
	templates := HostingCatalogTemplates()
	before := HostingDocumentHash(templates)
	for _, template := range templates {
		r := CatalogRequirements(template)
		if r.ArchitectureRestriction != "undeclared" || r.ArchitectureVerification != "unknown" || r.CPU != "undeclared" || r.Memory != "undeclared" || r.Disk != "undeclared" || len(r.MountPaths) == 0 {
			t.Fatalf("invented or missing requirements %+v", r)
		}
		if (r.SourceKind == "compose") != (template.Definition.Compose != nil) || r.PublicURLRequired != (template.Definition.Compose != nil) {
			t.Fatal("wrong source needs")
		}
		if len(r.ManagedCredentialNames) > 0 {
			r.ManagedCredentialNames[0] = "changed"
		}
	}
	if HostingDocumentHash(templates) != before || HostingDocumentHash(HostingCatalogTemplates()) != before {
		t.Fatal("requirements modified immutable template")
	}
}
