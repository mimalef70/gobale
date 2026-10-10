// Command providercatalog exports the same finite native contracts used by
// admission. Documentation generation never needs live credentials or clients.
package main

import (
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers/bale"
	"github.com/mimalef70/goomni/src/internal/eitaameow"
	"github.com/mimalef70/goomni/src/internal/rubikameow"
	"os"
	"sort"
)

func main() {
	catalogs := map[domains.Provider][]domains.OperationContract{}
	for _, contract := range []domains.ProviderContract{bale.Contract{}, eitaameow.Contract{}, rubikameow.Contract{}} {
		list := contract.Operations()
		sort.Slice(list, func(i, j int) bool { return list[i].Operation < list[j].Operation })
		catalogs[contract.Descriptor().ID] = list
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(catalogs); err != nil {
		panic(err)
	}
}
