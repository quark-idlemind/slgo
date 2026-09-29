package pay

import (
	"reflect"
	"regexp"
	"sort"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// A message with a field that looks like L$ is either one a program can
// spend with, and Spends says so, or one that only says a price -- setting
// what is for sale, describing an item, searching -- and is listed here
// with which.  A new message in the template that carries a price and is
// neither fails until somebody decides.
// Why: doc/money.md#what-else-spends-l
var onlySaysAPrice = map[string]string{
	"DirLandQuery":           "a search filter",
	"ObjectSaleInfo":         "sets the price of the avatar's own object",
	"ParcelPropertiesUpdate": "sets the price of the avatar's own parcel and its pass",
	"UpdateInventoryItem":    "sets an inventory item's sale price",
	"UpdateTaskInventory":    "sets the sale price of an item in the avatar's own object",
	"RezObject":              "the item's own sale price, as inventory describes it",
	"RezScript":              "the item's own sale price, as inventory describes it",
	"RezRestoreToWorld":      "the item's own sale price, as inventory describes it",
	"UpdateGroupInfo":        "sets the fee for joining the avatar's own group",
	"GodUpdateRegionInfo":    "a god tool, setting the land price per metre of a region",
	"RegionInfo":             "the estate's land price per metre, which the simulator reports and an estate owner sets",
}

var looksLikeMoney = regexp.MustCompile(`Price|Fee|Cost|Charge|Amount|Money|Balance`)

func hasMoneyField(t reflect.Type) (string, bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		ft := f.Type
		if ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			if name, ok := hasMoneyField(ft); ok {
				return name, true
			}
			continue
		}
		if looksLikeMoney.MatchString(f.Name) && ft.Kind() != reflect.Uint8 {
			return f.Name, true
		}
	}
	return "", false
}

func TestEveryMessageThatCarriesAPriceIsCheckedOrListed(t *testing.T) {
	var found []string
	for _, name := range msg.Names() {
		info := msg.LookupName(name)
		if info.Trusted {
			continue // the simulator's: a client's is ignored
		}
		m := msg.New(info.ID)
		if m == nil {
			continue
		}
		field, ok := hasMoneyField(reflect.TypeOf(m).Elem())
		if !ok || Spends(info.ID) {
			continue
		}
		if _, listed := onlySaysAPrice[name]; !listed {
			found = append(found, name+" ("+field+")")
		}
	}
	sort.Strings(found)
	for _, f := range found {
		t.Errorf("%s carries what looks like L$ and is neither checked by Spends nor listed as only saying a price", f)
	}
	for name := range onlySaysAPrice {
		if info := msg.LookupName(name); info == nil {
			t.Errorf("%s is listed and is not in the template", name)
		} else if Spends(info.ID) {
			t.Errorf("%s is listed as only saying a price, and is checked", name)
		} else if _, ok := hasMoneyField(reflect.TypeOf(msg.New(info.ID)).Elem()); !ok {
			t.Errorf("%s is listed as saying a price and carries none", name)
		}
	}
}
