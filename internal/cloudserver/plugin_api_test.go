package cloudserver

import (
	"testing"

	"github.com/0xmarkhydra/codelocal/internal/cloud"
	plugindomain "github.com/0xmarkhydra/codelocal/internal/plugins"
)

func TestPluginCatalogResponseKeepsManagedPenpotInstalled(t *testing.T) {
	response, err := pluginCatalogResponse(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.InstalledCount != 1 {
		t.Fatalf("installed count=%d want default Penpot only", response.InstalledCount)
	}
	var penpot *pluginCatalogItemDTO
	for index := range response.Items {
		if response.Items[index].ID == "penpot" {
			penpot = &response.Items[index]
		}
	}
	if penpot == nil || !penpot.System || !penpot.Installed || !penpot.SetupRequired || penpot.InstallationState != "system" || penpot.ServerName != "penpot" {
		t.Fatalf("unexpected Penpot system plugin: %#v", penpot)
	}
}

func TestPluginCatalogResponseDetectsManagedManifestDrift(t *testing.T) {
	entry, ok := plugindomain.FindBuiltin("penpot")
	if !ok {
		t.Fatal("penpot plugin missing")
	}
	response, err := pluginCatalogResponse([]cloud.PluginInstallation{{
		UserID: "user-1", PluginID: "penpot", Version: entry.Manifest.Version,
		ManifestHash: "stale-hash", State: cloud.PluginInstalled,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range response.Items {
		if item.ID == "penpot" && !item.UpdateAvailable {
			t.Fatalf("manifest drift must require update: %#v", item)
		}
	}
}
