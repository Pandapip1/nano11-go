package main

import (
	"errors"
	"fmt"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/service"
)

// servicesToRemove is nano11builder.ps1's $servicesToRemove list verbatim
// (its own commented-out entries -- DPS, Audiosrv, AudioEndpointBuilder,
// WbioSrvc -- are left out here too, along with the same inline warnings
// about why: removing Audiosrv/AudioEndpointBuilder is a likely cause of
// boot failure, and WbioSrvc can hang the logon screen).
var servicesToRemove = []string{
	"Spooler",
	"PrintNotify",
	"Fax",
	"RemoteRegistry",
	"diagsvc",
	"WerSvc",
	"PcaSvc",
	"MapsBroker",
	"WalletService",
	"BthAvctpSvc",
	"BluetoothUserService",
	"wuauserv",
	"UsoSvc",
	"WaaSMedicSvc",
}

// removeBloatServices deletes each of servicesToRemove's Services\<name>
// subkey entirely (nano11builder.ps1's second registry pass, "Loading
// registry hives to remove services") -- a stronger action than
// applyRegistryTweaks' Start=4 disables, and ported separately to mirror
// the original script's own separate pass.
func removeBloatServices(systemRoot *regf.Key) error {
	ccs, err := service.CurrentControlSet(systemRoot)
	if err != nil {
		return fmt.Errorf("resolve CurrentControlSet: %w", err)
	}
	servicesKey := ccs.FindOrCreateSubkey("Services")
	for _, name := range servicesToRemove {
		fmt.Printf("removing service: %s\n", name)
		if err := service.Delete(servicesKey, name); err != nil && !errors.Is(err, service.ErrNotFound) {
			return fmt.Errorf("remove service %s: %w", name, err)
		}
	}
	return nil
}
