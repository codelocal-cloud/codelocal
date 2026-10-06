package plugins

import "testing"

func TestBuiltinCatalogManifestsValidate(t *testing.T) {
	catalog := BuiltinCatalog()
	if len(catalog) < 1 {
		t.Fatalf("catalog size=%d want at least 1", len(catalog))
	}
	seen := map[string]bool{}
	for _, entry := range catalog {
		if err := ValidateManifest(entry.Manifest); err != nil {
			t.Fatalf("manifest %s invalid: %v", entry.Manifest.ID, err)
		}
		if seen[entry.Manifest.ID] {
			t.Fatalf("duplicate plugin id %q", entry.Manifest.ID)
		}
		seen[entry.Manifest.ID] = true
		if _, err := ManifestHash(entry.Manifest); err != nil {
			t.Fatalf("manifest hash failed for %s: %v", entry.Manifest.ID, err)
		}
		capabilities := ManifestCapabilities(entry.Manifest)
		if len(capabilities) == 0 {
			t.Fatalf("plugin %s exposes no capabilities", entry.Manifest.ID)
		}
	}
}

func TestPenpotIsDefaultSystemPluginBoundToManagedMCP(t *testing.T) {
	entry, ok := FindBuiltin("penpot")
	if !ok {
		t.Fatal("Penpot system plugin missing")
	}
	if !entry.DefaultInstalled || entry.Manifest.Scope != ScopeSystem || entry.Runtime == nil || entry.Runtime.ServerName != "penpot" {
		t.Fatalf("unexpected Penpot system plugin: %#v", entry)
	}
	if len(entry.Runtime.Targets) != 2 || entry.Runtime.Targets[0] != ExecutionLocal || entry.Runtime.Targets[1] != ExecutionCloud {
		t.Fatalf("unexpected Penpot execution targets: %#v", entry.Runtime.Targets)
	}
	if len(entry.Manifest.Components) != 1 || entry.Manifest.Components[0].Kind != ComponentAppTemplate || entry.Manifest.Components[0].AppTemplate == nil {
		t.Fatalf("Penpot must require a hosted MCP key connection: %#v", entry.Manifest.Components)
	}
}

func TestManifestCapabilitiesAreUniqueAndSorted(t *testing.T) {
	entry, ok := FindBuiltin("penpot")
	if !ok {
		t.Fatal("penpot plugin missing")
	}
	capabilities := ManifestCapabilities(entry.Manifest)
	for i := 1; i < len(capabilities); i++ {
		if capabilities[i-1] >= capabilities[i] {
			t.Fatalf("capabilities not unique/sorted: %#v", capabilities)
		}
	}
}
