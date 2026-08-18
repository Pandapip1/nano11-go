package main

import (
	"fmt"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/registry"
	"github.com/Pandapip1/gowim/service"
)

// dw/sz are small helpers for the SetValue calls below, reading like the
// reg add commands they mirror ('/t REG_DWORD /d N' / '/t REG_SZ /d S').
func dw(key *regf.Key, name string, n uint32) { key.SetValue(name, regf.RegDWORD, regf.EncodeDWORD(n)) }
func sz(key *regf.Key, name, s string)        { key.SetValue(name, regf.RegSZ, regf.EncodeSZ(s)) }

// applyRegistryTweaks ports nano11builder.ps1's entire registry-editing
// section (lines ~315-449: requirement bypass, sponsored-apps/telemetry/
// Copilot/Teams/Outlook/Update/Defender policy tweaks) against an already
// -loaded HiveSet's SOFTWARE/SYSTEM/DEFAULT/NTUSER.DAT hives, applying each
// "reg add"/"reg delete" 1:1 as a regf.Key operation.
func applyRegistryTweaks(hs *registry.HiveSet) error {
	software := hs.Hives[registry.HiveSoftware].Hive.Root
	system := hs.Hives[registry.HiveSystem].Hive.Root
	def := hs.Hives[registry.HiveDefault].Hive.Root
	ntuser := hs.Hives[registry.HiveDefaultUser].Hive.Root

	ccs, err := service.CurrentControlSet(system)
	if err != nil {
		return fmt.Errorf("resolve CurrentControlSet: %w", err)
	}

	fmt.Println("Bypassing system requirements...")
	dw(def.FindOrCreatePath(`Control Panel\UnsupportedHardwareNotificationCache`), "SV1", 0)
	dw(def.FindOrCreatePath(`Control Panel\UnsupportedHardwareNotificationCache`), "SV2", 0)
	dw(ntuser.FindOrCreatePath(`Control Panel\UnsupportedHardwareNotificationCache`), "SV1", 0)
	dw(ntuser.FindOrCreatePath(`Control Panel\UnsupportedHardwareNotificationCache`), "SV2", 0)
	labConfig := system.FindOrCreatePath(`Setup\LabConfig`)
	dw(labConfig, "BypassCPUCheck", 1)
	dw(labConfig, "BypassRAMCheck", 1)
	dw(labConfig, "BypassSecureBootCheck", 1)
	dw(labConfig, "BypassStorageCheck", 1)
	dw(labConfig, "BypassTPMCheck", 1)
	dw(system.FindOrCreatePath(`Setup\MoSetup`), "AllowUpgradesWithUnsupportedTPMOrCPU", 1)

	fmt.Println("Disabling sponsored apps...")
	cdm := ntuser.FindOrCreatePath(`SOFTWARE\Microsoft\Windows\CurrentVersion\ContentDeliveryManager`)
	for _, v := range []string{
		"OemPreInstalledAppsEnabled", "PreInstalledAppsEnabled", "SilentInstalledAppsEnabled",
		"ContentDeliveryAllowed", "FeatureManagementEnabled", "PreInstalledAppsEverEnabled",
		"SoftLandingEnabled", "SubscribedContentEnabled", "SubscribedContent-310093Enabled",
		"SubscribedContent-338388Enabled", "SubscribedContent-338389Enabled",
		"SubscribedContent-338393Enabled", "SubscribedContent-353694Enabled",
		"SubscribedContent-353696Enabled", "SystemPaneSuggestionsEnabled",
	} {
		dw(cdm, v, 0)
	}
	cdm.DeleteSubkey("Subscriptions")
	cdm.DeleteSubkey("SuggestedApps")
	cloudContent := software.FindOrCreatePath(`Policies\Microsoft\Windows\CloudContent`)
	dw(cloudContent, "DisableWindowsConsumerFeatures", 1)
	dw(cloudContent, "DisableConsumerAccountStateContent", 1)
	dw(cloudContent, "DisableCloudOptimizedContent", 1)
	sz(software.FindOrCreatePath(`Microsoft\PolicyManager\current\device\Start`), "ConfigureStartPins", `{"pinnedList": [{}]}`)
	dw(software.FindOrCreatePath(`Policies\Microsoft\PushToInstall`), "DisablePushToInstall", 1)
	dw(software.FindOrCreatePath(`Policies\Microsoft\MRT`), "DontOfferThroughWUAU", 1)

	fmt.Println("Enabling local accounts on OOBE...")
	dw(software.FindOrCreatePath(`Microsoft\Windows\CurrentVersion\OOBE`), "BypassNRO", 1)

	fmt.Println("Disabling reserved storage...")
	dw(software.FindOrCreatePath(`Microsoft\Windows\CurrentVersion\ReserveManager`), "ShippedWithReserves", 0)

	fmt.Println("Disabling BitLocker device encryption...")
	dw(ccs.FindOrCreatePath(`Control\BitLocker`), "PreventDeviceEncryption", 1)

	fmt.Println("Disabling Chat icon...")
	dw(software.FindOrCreatePath(`Policies\Microsoft\Windows\Windows Chat`), "ChatIcon", 3)
	dw(ntuser.FindOrCreatePath(`SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer\Advanced`), "TaskbarMn", 0)

	fmt.Println("Removing Edge-related registries...")
	software.DeletePath(`WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\Microsoft Edge`)
	software.DeletePath(`WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\Microsoft Edge Update`)

	fmt.Println("Disabling OneDrive folder backup...")
	dw(software.FindOrCreatePath(`Policies\Microsoft\Windows\OneDrive`), "DisableFileSyncNGSC", 1)

	fmt.Println("Disabling telemetry...")
	dw(ntuser.FindOrCreatePath(`Software\Microsoft\Windows\CurrentVersion\AdvertisingInfo`), "Enabled", 0)
	dw(ntuser.FindOrCreatePath(`Software\Microsoft\Windows\CurrentVersion\Privacy`), "TailoredExperiencesWithDiagnosticDataEnabled", 0)
	dw(ntuser.FindOrCreatePath(`Software\Microsoft\Speech_OneCore\Settings\OnlineSpeechPrivacy`), "HasAccepted", 0)
	dw(ntuser.FindOrCreatePath(`Software\Microsoft\Input\TIPC`), "Enabled", 0)
	inputPersonalization := ntuser.FindOrCreatePath(`Software\Microsoft\InputPersonalization`)
	dw(inputPersonalization, "RestrictImplicitInkCollection", 1)
	dw(inputPersonalization, "RestrictImplicitTextCollection", 1)
	dw(ntuser.FindOrCreatePath(`Software\Microsoft\InputPersonalization\TrainedDataStore`), "HarvestContacts", 0)
	dw(ntuser.FindOrCreatePath(`Software\Microsoft\Personalization\Settings`), "AcceptedPrivacyPolicy", 0)
	dw(software.FindOrCreatePath(`Policies\Microsoft\Windows\DataCollection`), "AllowTelemetry", 0)
	dw(ccs.FindOrCreatePath(`Services\dmwappushservice`), "Start", 4)

	fmt.Println("Preventing installation of DevHome and Outlook...")
	uscheduler := software.FindOrCreatePath(`Microsoft\Windows\CurrentVersion\WindowsUpdate\Orchestrator\UScheduler`)
	dw(uscheduler.FindOrCreateSubkey("OutlookUpdate"), "workCompleted", 1)
	dw(uscheduler.FindOrCreateSubkey("DevHomeUpdate"), "workCompleted", 1)
	software.DeletePath(`Microsoft\WindowsUpdate\Orchestrator\UScheduler_Oobe\OutlookUpdate`)
	software.DeletePath(`Microsoft\WindowsUpdate\Orchestrator\UScheduler_Oobe\DevHomeUpdate`)

	fmt.Println("Disabling Copilot...")
	dw(software.FindOrCreatePath(`Policies\Microsoft\Windows\WindowsCopilot`), "TurnOffWindowsCopilot", 1)
	dw(software.FindOrCreatePath(`Policies\Microsoft\Edge`), "HubsSidebarEnabled", 0)
	dw(software.FindOrCreatePath(`Policies\Microsoft\Windows\Explorer`), "DisableSearchBoxSuggestions", 1)

	fmt.Println("Preventing installation of Teams...")
	dw(software.FindOrCreatePath(`Policies\Microsoft\Teams`), "DisableInstallation", 1)

	fmt.Println("Preventing installation of New Outlook...")
	dw(software.FindOrCreatePath(`Policies\Microsoft\Windows\Windows Mail`), "PreventRun", 1)

	fmt.Println("Disabling Windows Update...")
	runOnce := software.FindOrCreatePath(`Microsoft\Windows\CurrentVersion\RunOnce`)
	sz(runOnce, "StopWUPostOOBE1", "net stop wuauserv")
	sz(runOnce, "StopWUPostOOBE2", "sc stop wuauserv")
	sz(runOnce, "StopWUPostOOBE3", "sc config wuauserv start= disabled")
	sz(runOnce, "DisbaleWUPostOOBE1", `reg add HKLM\SYSTEM\CurrentControlSet\Services\wuauserv /v Start /t REG_DWORD /d 4 /f`)
	sz(runOnce, "DisbaleWUPostOOBE2", `reg add HKLM\SYSTEM\ControlSet001\Services\wuauserv /v Start /t REG_DWORD /d 4 /f`)
	wuPolicy := software.FindOrCreatePath(`Policies\Microsoft\Windows\WindowsUpdate`)
	dw(wuPolicy, "DoNotConnectToWindowsUpdateInternetLocations", 1)
	dw(wuPolicy, "DisableWindowsUpdateAccess", 1)
	sz(wuPolicy, "WUServer", "localhost")
	sz(wuPolicy, "WUStatusServer", "localhost")
	sz(wuPolicy, "UpdateServiceUrlAlternate", "localhost")
	dw(software.FindOrCreatePath(`Policies\Microsoft\Windows\WindowsUpdate\AU`), "UseWUServer", 1)
	dw(software.FindOrCreatePath(`Policies\Microsoft\Windows\WindowsUpdate\AU`), "NoAutoUpdate", 1)
	dw(software.FindOrCreatePath(`Microsoft\Windows\CurrentVersion\OOBE`), "DisableOnline", 1)
	dw(ccs.FindOrCreatePath(`Services\wuauserv`), "Start", 4)
	ccs.DeletePath(`Services\WaaSMedicSVC`)
	ccs.DeletePath(`Services\UsoSvc`)

	fmt.Println("Disabling Windows Defender...")
	for _, svcName := range []string{"WinDefend", "WdNisSvc", "WdNisDrv", "WdFilter", "Sense"} {
		if err := service.Disable(ccs.FindOrCreateSubkey("Services"), svcName); err != nil {
			fmt.Printf("warning: disable service %s: %v\n", svcName, err)
		}
	}
	sz(software.FindOrCreatePath(`Microsoft\Windows\CurrentVersion\Policies\Explorer`), "SettingsPageVisibility", "hide:virus;windowsupdate")

	return nil
}
