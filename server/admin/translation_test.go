package admin

import (
	"errors"
	"testing"
)

func TestTranslationSettingsAcceptMultipleProvidersAndRoutes(t *testing.T) {
	control, err := NewControlPlane(&memoryRepository{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := control.Snapshot()
	settings := snapshot.Settings
	settings.Translation.Enabled = true
	settings.Translation.Providers = []TranslationProviderSettings{
		{
			ID: "azure-primary", Type: "azure", Enabled: true, Priority: 10,
			CredentialFile: "/run/secrets/translate-azure", Region: "westeurope",
		},
		{
			ID: "libre-backup", Type: "libretranslate", Enabled: true, Priority: 20,
			Endpoint: "https://translate.example.com",
		},
	}
	settings.Translation.Routes = []TranslationRouteSettings{
		{Source: "zh", Target: "en", Providers: []string{"azure-primary", "libre-backup"}},
		{Source: "en", Target: "zh-CN", Providers: []string{"libre-backup", "azure-primary"}},
	}
	updated, err := control.UpdateSettings(snapshot.Version, settings, Actor{Subject: "test-admin"})
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.Settings.Translation.Providers[0].TimeoutMS; got != 1500 {
		t.Fatalf("provider defaults were not applied: timeout=%d", got)
	}
}

func TestTranslationSettingsRejectUnsafeOrUnusableConfiguration(t *testing.T) {
	control, err := NewControlPlane(&memoryRepository{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := control.Snapshot()
	settings := snapshot.Settings
	settings.Translation.Enabled = true
	if _, err = control.UpdateSettings(snapshot.Version, settings, Actor{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("enabled translation without providers must fail, got %v", err)
	}

	settings.Translation.Providers = []TranslationProviderSettings{{
		ID: "bad-provider", Type: "azure", Enabled: true,
		CredentialFile: "literal-secret-value!", Endpoint: "file:///etc/passwd",
	}}
	if _, err = control.UpdateSettings(snapshot.Version, settings, Actor{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe provider configuration must fail, got %v", err)
	}
}

func TestLegacyTranslationFlagFailsClosedDuringUpgrade(t *testing.T) {
	document := defaultDocument()
	document.Settings.Translation = TranslationSettings{
		Enabled: true, StaffLanguage: "zh-CN", CustomerLanguage: "en", KeepOriginal: true,
	}
	control, err := NewControlPlane(&memoryRepository{document: document})
	if err != nil {
		t.Fatal(err)
	}
	if control.Snapshot().Settings.Translation.Enabled {
		t.Fatal("legacy enabled flag without providers must be disabled")
	}
}
